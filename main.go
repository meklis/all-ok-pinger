package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/go-ini/ini"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"
)

const (
	ICMP_RESPONSE_BUFFER_BYTES = 1024
	ProtocolICMP               = 1
	ProtocolIPv6ICMP           = 58
)

type LoadJson struct {
	Code int `json:"code"`
	Data []struct {
		IP     string `json:"ip"`
		Status int    `json:"status"`
	} `json:"data"`
}
type ResponceJson struct {
	Time     string
	Data     map[string]int
	ServerID int
}
type Config struct {
	ServerID            int    //ID пинговалки
	HostList            string //Ссылка, откуда вытягивать список для пинга
	ReportTo            string //Забрасывать результаты ходилки
	PackageForHost      int    //Кол. пакетов для отправки на 1 хост
	TimeOutResponce     int    //Время ожидания ответа
	UseSocket           int    //Использовать сокеты для проверки работы
	NumberOfInspection  int    //Количество повторных обходов
	SocketPorts         string //Список портов, по которым нужно пройтись для проверки
	SleepAfrer          int    //Таймаут после завершения прохода
	SocketTimeOut       int    //Макс время ожидания ответа
	MaxThreadsForSocket int    //Каждый хост обрабатывается в отдельном потоке, здесь ограничиваем потоки
	MustPackageForUp    int    //Необходимое количество пакетов для поднятия хоста
	CountListener       int    //Количество одновременно запущенных ReadSocket
	EnaIPv6             int
	MinInspectionForUp  int // Минимальное количество вхождений для поднятия
}

var (
	conf Config //Конфиг
	l    = log.Printf
	f    = fmt.Sprintf
	p    = fmt.Println
)

func main() {

	//Заюзаем все ядра для пинга
	numcpu := runtime.NumCPU()
	runtime.GOMAXPROCS(numcpu)

	//Прочитаем конфиг
	LoadConfig()

	//Делаем бесконечный цикл, что бы прогамма не завершалась
	for {
		startTime := time.Now()
		//Получим список IP на пинг
		data, status := GetIpsList()

		//Если лист получен
		if status {

			//Сюда записываем результаты прохода
			resultList := make(map[string]int)

			//Предварительно апнувшийся узлы
			pregResult := make(map[string]int)

			//Здесь будет находится список для пинга, для всех проходов
			forPing := make(map[string]int)

			//Перебираем JSON массив
			for _, dd := range data.Data {
				forPing[dd.IP] = dd.Status
			}
			l("Получено хостов для пинга с базы %v", len(forPing))

			//Делаем повторные обходы для фиксации данных
			for i := 0; i < conf.NumberOfInspection; i++ {
				//Запишем ответившие хосты в resp
				resp := startPing(forPing, conf.TimeOutResponce)
				time.Sleep(time.Millisecond * 50)
				l("Всего хостов ответило: %v", len(resp))
				for ip, count := range forPing {

					//Если узел лежал и поднялся - плюсанем ему счетчик
					if count == 0 && resp[ip] != 0 {
						if resp[ip] >= conf.MustPackageForUp {
							pregResult[ip]++
						}
					}

					//ХОСТ НЕ ИЗМЕНИЛ СВОЕ СОСТОЯНИЕ

					//Как лежал, так и лежит
					if count == 0 && resp[ip] == 0 {
						delete(forPing, ip) //Свитч не изменил состояние - удалим с повторного пинга
					}

					//Как работал, так и работает
					if resp[ip] != 0 && count != 0 {
						delete(forPing, ip) //Свитч не изменил состояние - удалим с повторного пинга
					}
				}

				l("Проход #%v, предварительно изменений: %v", i, len(pregResult))
			}

			//Если все проходы идеально ответил - апнем узел
			for ip, countRepeat := range pregResult {
				if countRepeat >= conf.MinInspectionForUp {
					resultList[ip] = 1
				}
				delete(forPing, ip) //Удалим из списка, что бы случайно не уложить
			}
			l("Апнулось %v хостов", len(resultList))

			//TCP connector по хостам, которые собираемся ложить
			if conf.UseSocket == 1 && len(forPing) != 0 {
				l("Включено использование сокетов, на проверку %v хостов", len(forPing))
				tcpResponse := checkingByTCP(forPing)
				l("Ответило %v хостов на TCP коннекты", len(tcpResponse))
				for tcp_host, _ := range tcpResponse {
					//resultList[tcp_host] = tcp_status
					delete(forPing, tcp_host)
				}
			}

			//Остались только упавшие хосты, их нужно заапдейтить
			for host, _ := range forPing {
				resultList[host] = 0
			}

			l("Положили узлов %v", len(forPing))
			l("На отправку к серверу %v изменений", len(resultList))
			sendResult(resultList)
			stopTime := time.Since(startTime).Seconds()
			l("Весь цикл отработал за %v сек", stopTime)
			l("Результат отправили, ждем %v сек\n\n", conf.SleepAfrer)
			time.Sleep(time.Second * time.Duration(conf.SleepAfrer))

		} else {
			log.Printf("Не удалось получить данные Json с сервера. Повторная попытка через 30 сек...")
			time.Sleep(time.Second * 30)
		}
	}
}

func sendResult(result map[string]int) {
	var res ResponceJson
	res.Data = result
	var t = time.Now()
	res.Time = f("%d-%02d-%02d %02d:%02d:%02d", t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
	res.ServerID = conf.ServerID
	res_json, _ := json.Marshal(res) //Запакуем в Json

	req, err := http.NewRequest("POST", conf.ReportTo, bytes.NewBuffer(res_json))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		l(f("НЕ УДАЛОСЬ ОТПРАВИТЬ ДАННЫЕ: %v", err))
	} else {
		defer resp.Body.Close()
	}
}
func GetIpsList() (data LoadJson, errLn bool) {
	errLn = false
	log.Printf("GetIpsList: Получение списка...")
	response, err := http.Get(f("%v?serverID=%v", conf.HostList, conf.ServerID))
	if err != nil {
		return data, false
	}
	defer response.Body.Close()
	values, _ := ioutil.ReadAll(response.Body)
	err = json.Unmarshal(values, &data)
	if err != nil {
		log.Println(err)
	}
	if err != nil || len(data.Data) == 0 {
		return data, false
	}
	return data, true
}
func writeSocket(ip string, c *icmp.PacketConn) {
	for i := 0; i < conf.PackageForHost; i++ {

		//Создадим ICMP пакет
		wm := icmp.Message{
			Type: ipv4.ICMPTypeEcho, Code: 0,
			Body: &icmp.Echo{
				ID: os.Getpid() & 0xffff, Seq: i,
				Data: []byte(ip),
			},
		}

		//Переведем ICMP пакет в байты
		wb, err := wm.Marshal(nil)
		if err != nil {
			l(f("%v", err))
		}

		//Закидываем пакет в сокет
		if _, err := c.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(ip)}); err != nil {
			l("Сокет переполнен! Ожидание 10мс перед повтором")
			time.Sleep(time.Millisecond * 10)
		}
		time.Sleep(time.Microsecond * 5)
	}
	time.Sleep(time.Microsecond * 5)
}
func writeSocket6(ip string, c *icmp.PacketConn) {
	for i := 0; i < conf.PackageForHost; i++ {

		//Создадим ICMP пакет
		wm := icmp.Message{
			Type: ipv6.ICMPTypeEchoRequest, Code: 0,
			Body: &icmp.Echo{
				//ID: os.Getpid() & 0xffff, Seq: i,
				ID: rand.Intn(65535), Seq: i,
				Data: []byte(ip),
			},
		}

		//Переведем ICMP пакет в байты
		wb, err := wm.Marshal(nil)
		if err != nil {
			l(f("%v", err))
		}

		//Закидываем пакет в сокет
		if _, err := c.WriteTo(wb, &net.IPAddr{IP: net.ParseIP(ip)}); err != nil {
			if strings.Contains(err.Error(), "no buffer space available") {
				l("Ошибка записи в сокет! Ожидание 10мс перед повтором")
				time.Sleep(time.Millisecond * 10)
			}
		}
		time.Sleep(time.Microsecond * 5)
	}
	time.Sleep(time.Microsecond * 10)
}
func readSocket(c *icmp.PacketConn, resp chan string, signal chan bool) {
	rb := make([]byte, 1500)
Loop:
	for {

		//Если пришел сигнал завершения - выходим из цикла
		if len(signal) != 0 {
			l("Слушатель сокета IPv4 получил сигнал завершения")
			return
		}

		//Читаем с сокета данные в буфер
		n, peer, err := c.ReadFrom(rb)

		//Если при чтении возни
		if err != nil {
			continue Loop
		}

		//Парсим прочитаный пакет
		rm, err := icmp.ParseMessage(ProtocolICMP, rb[:n])
		if err != nil {
			log.Printf(f("%v", err))
			continue Loop
		}

		//Обрабатываем результат
		switch rm.Type {
		case ipv4.ICMPTypeEchoReply:
			resp <- f("%v", peer)
		default:
		}

	}
}
func readSocket6(c *icmp.PacketConn, resp chan string, signal chan bool) {
	rb := make([]byte, 1500)
Loop:
	for {

		//Если пришел сигнал завершения - выходим из цикла
		if len(signal) != 0 {
			l("Слушатель сокета IPv6 получил сигнал завершения")
			return
		}

		//Читаем с сокета данные в буфер
		n, peer, err := c.ReadFrom(rb)

		//Если при чтении возни
		if err != nil {
			continue Loop
		}
		//p(n, peer)
		//Парсим прочитаный пакет
		rm, err := icmp.ParseMessage(ProtocolIPv6ICMP, rb[:n])
		if err != nil {
			log.Printf(f("%v", err))
			continue Loop
		}
		//p(rm.Type)
		//Обрабатываем результат
		switch rm.Type {
		case ipv6.ICMPTypeEchoReply:
			resp <- f("%v", peer)
		default:
		}

	}
}
func startPing(data map[string]int, timeout int) (resp_list map[string]int) {
	l("Запустили пинговалку IPv4/IPv6...")

	//Создадим хэш для ответов
	resp_list = make(map[string]int)

	//Откроем сокет
	socket, err := icmp.ListenPacket("ip4:icmp", "")
	if err != nil {
		log.Printf("startPing: listen err, %s", err)
	}
	//Откроем сокет V6
	socket6, err := icmp.ListenPacket("ip6:ipv6-icmp", "")
	if err != nil {
		log.Printf("startPing6: listen err, %s", err)
	}

	//Закроем сокет после завершения функции
	defer socket.Close()
	defer socket6.Close()

	//Создадим общие пулы
	var response chan string = make(chan string, 5000000)
	var signal chan bool = make(chan bool, 1)

	//Запустили слушателя
	for i := 0; i < conf.CountListener; i++ {
		go readSocket(socket, response, signal)
		if conf.EnaIPv6 == 1 {
			go readSocket6(socket6, response, signal)
		}
	}
	//Закидываем в сокет пакеты
	for v, _ := range data {
		if strings.Contains(v, ".") {
			writeSocket(v, socket)
		} else if conf.EnaIPv6 == 1 && strings.Contains(v, ":") {
			writeSocket6(v, socket6)
		} else if !strings.Contains(v, ":") {
		} else {
			l("IP: %v не пинговался, выключен IPv6", v)
		}
	}

	//Говорим сокету, сколько ему еще ждать ответа
	socket.SetReadDeadline(time.Now().Add(time.Second * time.Duration(timeout)))

	//Ждем еще ответов
	resp_time := timeout + 1
	time.Sleep(time.Second * time.Duration(resp_time))

	//Говорим слушателю, что бы завершился
	signal <- true
	time.Sleep(time.Millisecond * 50)
	close(response)

	//Проанализируем ответы
	for ips_list := range response {
		resp_list[ips_list]++
	}

	return resp_list
}
func LoadConfig() {
	config_name := "./GoPinger.ini"
	if len(os.Args) > 1 {
		config_name = os.Args[1]
	}
	//Загружаем файлик
	cfg, err := ini.Load(config_name)
	if err != nil {
		p("Ошибка загрузки конфига!!!")
		os.Exit(1)
	}
	//Читаем конфиг
	conf.ServerID, _ = strconv.Atoi(cfg.Section("GoPinger").Key("ServerID").String())
	conf.HostList = cfg.Section("GoPinger").Key("HostList").String()
	conf.ReportTo = cfg.Section("GoPinger").Key("ReportTo").String()
	conf.PackageForHost, _ = strconv.Atoi(cfg.Section("GoPinger").Key("PackageForHost").String())
	conf.TimeOutResponce, _ = strconv.Atoi(cfg.Section("GoPinger").Key("TimeOutResponce").String())
	conf.UseSocket, _ = strconv.Atoi(cfg.Section("GoPinger").Key("UseSocket").String())
	conf.SocketPorts = cfg.Section("GoPinger").Key("SocketPorts").String()
	conf.NumberOfInspection, _ = strconv.Atoi(cfg.Section("GoPinger").Key("NumberOfInspection").String())
	conf.SleepAfrer, _ = strconv.Atoi(cfg.Section("GoPinger").Key("SleepAfrer").String())
	conf.SocketTimeOut, _ = strconv.Atoi(cfg.Section("GoPinger").Key("SocketTimeOut").String())
	conf.MaxThreadsForSocket, _ = strconv.Atoi(cfg.Section("GoPinger").Key("MaxThreadsForSocket").String())
	conf.MustPackageForUp, _ = strconv.Atoi(cfg.Section("GoPinger").Key("MustPackageForUp").String())
	conf.CountListener, _ = strconv.Atoi(cfg.Section("GoPinger").Key("CountListener").String())
	conf.EnaIPv6, _ = strconv.Atoi(cfg.Section("GoPinger").Key("EnaIPv6").String())
	conf.MinInspectionForUp, _ = strconv.Atoi(cfg.Section("GoPinger").Key("MinInspectionForUp").String())

	//Покажем, что загрузили
	l("CONF\tServerID: %v", conf.ServerID)
	l("CONF\tHostList: %v", conf.HostList)
	l("CONF\tReportTo: %v", conf.ReportTo)
	l("CONF\tPackageForHost: %v", conf.PackageForHost)
	l("CONF\tMustPackageForUp: %v", conf.MustPackageForUp)
	l("CONF\tTimeOutResponce: %v", conf.TimeOutResponce)
	l("CONF\tUseSocket: %v", conf.UseSocket)
	l("CONF\tSocketPorts: %v", conf.SocketPorts)
	l("CONF\tSleepAfter: %v", conf.SleepAfrer)
	l("CONF\tSocketTimeOut: %v", conf.SocketTimeOut)
	l("CONF\tMaxThreadsForSocket: %v", conf.MaxThreadsForSocket)
	l("CONF\tCountListener: %v", conf.CountListener)
	l("CONF\tEnaIPv6: %v", conf.EnaIPv6)
	l("CONF\tMinInspectionForUp: %v", conf.MinInspectionForUp)
}
func tcpTest(ip string, response chan string) {
	ports := strings.Split(conf.SocketPorts, ",")
	for _, port := range ports {
		conn, err := net.DialTimeout("tcp", ip+":"+port, time.Millisecond*time.Duration(conf.SocketTimeOut))
		if err == nil {
			conn.Close()
			response <- ip
			return
		}
	}
}
func wait(count int) {
	run := runtime.NumGoroutine()
	if run > count {
		l("Работает потоков -  %v, ждем завершения", run)
		time.Sleep(time.Second * 1)
		wait(count)
	} else {
		return
	}
}
func checkingByTCP(data map[string]int) (resp_list map[string]int) {
	resp_list = make(map[string]int)
	var response chan string = make(chan string, 5000000)

	//Закидываем свитчи на тест
	for v, _ := range data {
		go tcpTest(v, response)
		wait(conf.MaxThreadsForSocket)
	}
	wait(1)
	close(response)
	for ips_list := range response {
		resp_list[ips_list] = 1000
	}
	return resp_list
}
