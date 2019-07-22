package api

import (
	"bitbucket.org/meklis/helpprovider-gopinger/pinger"
	"github.com/imroc/req"
	"github.com/ztrue/tracerr"
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

func (c *API) GetHosts() ([]pinger.Device, error) {
	resp, err := req.Get(c.Config.HostListAddr+"?ident="+c.Config.PingerIdent, c.headers)
	if err != nil {
		return nil, tracerr.Wrap(err)
	}
	devices := make([]pinger.Device, 0)
	if err := resp.ToJSON(&devices); err != nil {
		return nil, tracerr.Wrap(err)
	}
	return devices, nil
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
	api := new(API)
	api.Config = conf
	api.headers = req.Header{
		"Accept": "application/json",
	}
	return api
}
