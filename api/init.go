package api

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"time"

	"github.com/imroc/req"
	"github.com/meklis/all-ok-pinger/pinger"
	"github.com/sirupsen/logrus"
	"github.com/ztrue/tracerr"
)

type Configuration struct {
	HostListAddr   string
	ReportAddr     string
	RequestTimeout time.Duration
	PingerIdent    string
	Logger         *logrus.Logger
}

type API struct {
	Config  Configuration
	headers req.Header
}

// ApiResponse - host list: {"data": [{"ip": .., "status": .., "target_group": .., "_attr": ..}]}
type ApiResponse struct {
	Data []pinger.Device `json:"data"`
}

func withIdent(addr, ident string) string {
	u, err := url.Parse(addr)
	if err != nil {
		return addr + "?ident=" + url.QueryEscape(ident)
	}
	q := u.Query()
	q.Set("ident", ident)
	u.RawQuery = q.Encode()
	return u.String()
}

func (c *API) GetHosts() ([]pinger.Device, error) {
	resp, err := req.Get(withIdent(c.Config.HostListAddr, c.Config.PingerIdent), c.headers)
	if err != nil {
		return nil, tracerr.Wrap(err)
	}
	if code := resp.Response().StatusCode; code != 200 {
		return nil, tracerr.Errorf("host list API returned %v: %v", code, truncate(resp.String(), 300))
	}
	data := ApiResponse{}
	if err := resp.ToJSON(&data); err != nil {
		return nil, tracerr.Errorf("error parse host list: %v, body: %v", err, truncate(resp.String(), 300))
	}
	return data.Data, nil
}

func (c *API) SendUpdate(objects []map[string]interface{}) error {
	addr := withIdent(c.Config.ReportAddr, c.Config.PingerIdent)
	if c.Config.Logger != nil {
		request, _ := json.Marshal(objects)
		c.Config.Logger.Debugf("Report URL: %v", addr)
		c.Config.Logger.Debugf("Sending statuses: %v", string(request))
	}
	resp, err := req.Post(addr, req.BodyJSON(objects), c.headers)
	if err != nil {
		return tracerr.Wrap(err)
	}
	body, err := resp.ToString()
	if err != nil {
		return tracerr.Errorf("Failed to read response body: %v", err)
	}
	if c.Config.Logger != nil {
		c.Config.Logger.Debugf("Response body: %v", body)
	}
	if resp.Response().StatusCode != 200 {
		return tracerr.Errorf("%v: %v", resp.Response().Status, truncate(body, 300))
	}
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
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
		"Content-Type": "application/json",
	}
	return api
}
