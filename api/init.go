package api

import (
	"crypto/tls"
	"github.com/imroc/req"
	"github.com/meklis/all-ok-pinger/pinger"
	"github.com/ztrue/tracerr"
	"net/http"
	"net/http/cookiejar"
	"time"
)

type Configuration struct {
	HostListAddr   string        `yaml:"host_list_addr"`
	ReportAddr     string        `yaml:"report_addr"`
	RequestTimeout time.Duration `yaml:"request_timeout"`
	PingerIdent    string        `yaml:"pinger_ident"`
}

type API struct {
	Config  Configuration
	headers req.Header
}
type ApiResponse struct {
	StatusCode int `json:"statusCode"`
	Meta       struct {
		Count int `json:"count"`
	} `json:"meta"`
	Data []pinger.Device `json:"data"`
}

func (c *API) GetHosts() ([]pinger.Device, error) {
	resp, err := req.Get(c.Config.HostListAddr+"?ident="+c.Config.PingerIdent, c.headers)
	if err != nil {
		return nil, tracerr.Wrap(err)
	}
	data := ApiResponse{}
	if err := resp.ToJSON(&data); err != nil {
		return nil, tracerr.Wrap(err)
	}
	return data.Data, nil
}

func (c *API) SendUpdate(dev []pinger.Device) error {
	resp, err := req.Post(c.Config.HostListAddr+"?ident="+c.Config.PingerIdent, req.BodyJSON(dev), c.headers)
	if err != nil {
		return tracerr.Wrap(err)
	}
	if resp.Response().StatusCode != 200 {
		return tracerr.Errorf("%v: %v", resp.Response().StatusCode, resp.Response().Status)
	}
	return nil
}

func NewApi(conf Configuration) *API {
	req.SetTimeout(conf.RequestTimeout)
	req.Client().Jar, _ = cookiejar.New(nil)
	trans, _ := req.Client().Transport.(*http.Transport)
	trans.MaxIdleConns = 20
	trans.TLSHandshakeTimeout = 20 * time.Second
	trans.DisableKeepAlives = true
	trans.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	api := new(API)
	api.Config = conf
	api.headers = req.Header{
		"Accept": "application/json",
	}
	return api
}
