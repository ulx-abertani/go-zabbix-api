package zabbix

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

type (
	// Params Zabbix request param
	Params map[string]interface{}
)

type request struct {
	Jsonrpc string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	Auth    string      `json:"auth,omitempty"`
	ID      int32       `json:"id"`
}

// Response format of zabbix api
type Response struct {
	Jsonrpc string      `json:"jsonrpc"`
	Error   *Error      `json:"error"`
	Result  interface{} `json:"result"`
	ID      int32       `json:"id"`
}

// RawResponse format of zabbix api
type RawResponse struct {
	Jsonrpc string          `json:"jsonrpc"`
	Error   *Error          `json:"error"`
	Result  json.RawMessage `json:"result"`
	ID      int32           `json:"id"`
}

// Error contains error data and code
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d (%s): %s", e.Code, e.Message, e.Data)
}

// ExpectedOneResult use to generate error when you expect one result
type ExpectedOneResult int

func (e *ExpectedOneResult) Error() string {
	return fmt.Sprintf("Expected exactly one result, got %d.", *e)
}

// ExpectedMore use to generate error when you expect more element
type ExpectedMore struct {
	Expected int
	Got      int
}

func (e *ExpectedMore) Error() string {
	return fmt.Sprintf("Expected %d, got %d.", e.Expected, e.Got)
}

// API use to store connection information
type API struct {
	Auth      string      // auth token, filled by Login()
	Logger    *log.Logger // request/response logger, nil by default
	UserAgent string
	url       string
	c         http.Client
	id        int32
	ex        sync.Mutex
	Config    Config
}

type Config struct {
	Url         string
	TlsNoVerify bool
	Log         *log.Logger
	Serialize   bool
	Version     int
}

func parseVersionString(vstr string) (version int64, err error) {
	parts := strings.Split(vstr, ".")

	version, err = strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return
	}
	version = version * 10000

	// do we have a minor version
	if len(parts) > 1 {
		var no int64
		no, err = strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			return
		}
		version += no * 100
	}

	// do we have a patch version
	if len(parts) > 2 {
		var no int64
		no, err = strconv.ParseInt(parts[2], 10, 64)
		if err != nil {
			return
		}
		version += no
	}
	return
}

// NewAPI Creates new API access object.
// Typical URL is http://host/api_jsonrpc.php or http://host/zabbix/api_jsonrpc.php.
// It also may contain HTTP basic auth username and password like
// http://username:password@host/api_jsonrpc.php.
func NewAPI(c Config) (api *API, err error) {
	api = &API{
		url:       c.Url,
		c:         http.Client{},
		UserAgent: "github.com/tpretz/go-zabbix-api",
		Logger:    c.Log,
		Config:    c,
	}

	if c.TlsNoVerify {
		tr := &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
		}
		api.c = http.Client{
			Transport: tr,
		}
		api.printf("TLS running in insecure mode, do not use this configuration in production")
	}

	var rawVersion string
	rawVersion, err = api.Version()
	if err != nil {
		return
	}
	var version int64
	version, err = parseVersionString(rawVersion)
	if err != nil {
		return
	}
	api.Config.Version = int(version)

	return
}

// SetClient Allows one to use specific http.Client, for example with InsecureSkipVerify transport.
func (api *API) SetClient(c *http.Client) {
	api.c = *c
}

func (api *API) printf(format string, v ...interface{}) {
	if api.Logger != nil {
		api.Logger.Printf(format, v...)
	}
}

func (api *API) callBytes(method string, params interface{}) (b []byte, err error) {
	id := atomic.AddInt32(&api.id, 1)
	jsonobj := request{
    Jsonrpc: "2.0",
    Method:  method,
    Params:  params,
    ID:      id,
	}
	

	b, err = json.Marshal(jsonobj)
	if err != nil {
		return
	}
	api.printf("Request (POST): %s", b)

	req, err := http.NewRequest("POST", api.url, bytes.NewReader(b))
	if err != nil {
		return
	}
	req.ContentLength = int64(len(b))
	req.Header.Add("Content-Type", "application/json-rpc")
	req.Header.Add("User-Agent", api.UserAgent)
	if api.Auth != "" {
    req.Header.Add("Authorization", fmt.Sprintf("Bearer %s", api.Auth))
	}


	if api.Config.Serialize {
		api.ex.Lock()
		defer api.ex.Unlock()
	}

	res, err := api.c.Do(req)
	if err != nil {
		api.printf("Error   : %s", err)
		return
	}
	defer res.Body.Close()

	b, err = ioutil.ReadAll(res.Body)
	api.printf("Response (%d): %s", res.StatusCode, b)
	return
}

// Call Calls specified API method. Uses api.Auth if not empty.
// err is something network or marshaling related. Caller should inspect response.Error to get API error.
func (api *API) Call(method string, params interface{}) (response Response, err error) {
	b, err := api.callBytes(method, params)
	if err == nil {
		err = json.Unmarshal(b, &response)
	}
	return
}

// CallWithError Uses Call() and then sets err to response.Error if former is nil and latter is not.
func (api *API) CallWithError(method string, params interface{}) (response Response, err error) {
	response, err = api.Call(method, params)
	if err == nil && response.Error != nil {
		err = response.Error
	}
	return
}

// CallWithErrorParse Calls specified API method.
// Parse the response of the api in the result variable.
func (api *API) CallWithErrorParse(method string, params interface{}, result interface{}) (err error) {
	var rawResult RawResponse

	response, err := api.callBytes(method, params)
	if err != nil {
		return
	}
	err = json.Unmarshal(response, &rawResult)
	if err != nil {
		return
	}
	if rawResult.Error != nil {
		return rawResult.Error
	}
	err = json.Unmarshal(rawResult.Result, &result)
	return
}

// Login Calls "user.login" API method and fills api.Auth field.
// This method modifies API structure and should not be called concurrently with other methods.
func (api *API) Login(user, password string) (auth string, err error) {
	var response Response
	if api.Config.Version >= 50400 {
		response, err = api.CallWithError("user.login", map[string]string{"username": user, "password": password})
	} else {
		response, err = api.CallWithError("user.login", map[string]string{"user": user, "password": password})
	}
	if err != nil {
		return
	}

	auth = response.Result.(string)
	api.Auth = auth
	return
}

// Version Calls "APIInfo.version" API method.
// This method temporary modifies API structure and should not be called concurrently with other methods.
func (api *API) Version() (v string, err error) {
	// temporary remove auth for this method to succeed
	// https://www.zabbix.com/documentation/2.2/manual/appendix/api/apiinfo/version
	auth := api.Auth
	api.Auth = ""
	response, err := api.CallWithError("APIInfo.version", Params{})
	api.Auth = auth

	// despite what documentation says, Zabbix 2.2 requires auth, so we try again
	if e, ok := err.(*Error); ok && e.Code == -32602 {
		response, err = api.CallWithError("APIInfo.version", Params{})
	}
	if err != nil {
		return
	}

	v = response.Result.(string)
	return
}
