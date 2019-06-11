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
)

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
