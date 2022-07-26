package apigateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
)

type RequestContext struct {
	AccountID    string `json:"accountId"`
	ApiId        string `json:"apiId"`
	DomainName   string `json:"domainName"`
	DomainPrefix string `json:"domainPrefix"`
	Http         struct {
		Method    string `json:"method"`
		Path      string `json:"path"`
		Protocol  string `json:"protocol"`
		SourceIp  string `json:"sourceIp"`
		UserAgent string `json:"userAgent"`
	} `json:"http"`
	QueryStringParameters map[string]string `json:"queryStringParameters"`
	RequestId             string            `json:"requestId"`
	RouteKey              string            `json:"routeKey"`
	Stage                 string            `json:"stage"`
	Time                  string            `json:"time"`
	TimeEpoch             int64             `json:"timeEpoch"`
}

type Request struct {
	Cookies         []string          `json:"cookies,omitempty"`
	Version         string            `json:"version,omitempty"`
	RouteKey        string            `json:"routeKey,omitempty"`
	RawPath         string            `json:"rawPath,omitempty"`
	RawQueryString  string            `json:"rawQueryString,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	RequestContext  RequestContext    `json:"requestContext,omitempty"`
	StageVariables  map[string]string `json:"stageVariables,omitempty"`
	Body            string            `json:"body,omitempty"`
	IsBase64Encoded bool              `json:"isBase64Encoded,omitempty"`

	// Version 1 Parameters
	HttpMethod            string            `json:"httpMethod,omitempty"`
	Path                  string            `json:"path,omitempty"`
	QueryStringParameters map[string]string `json:"queryStringParameters,omitempty"`
}

type Response struct {
	StatusCode      int               `json:"statusCode,omitempty"`
	Headers         map[string]string `json:"headers,omitempty"`
	Body            string            `json:"body,omitempty"`
	IsBase64Encoded bool              `json:"isBase64Encoded,omitempty"`
}

func Wrap(handler http.Handler, paths ...string) func(ctx context.Context, event Request) (Response, error) {
	prefix := filepath.Join(paths...)
	if len(prefix) > 0 && !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}

	return func(ctx context.Context, event Request) (Response, error) {
		var req *http.Request
		switch event.Version {
		case "2.0":
			v, err := makeV2Request(event, prefix)
			if err != nil {
				return Response{}, err
			}
			req = v
		default:
			v, err := makeV1Request(event)
			if err != nil {
				return Response{}, err
			}
			req = v
		}

		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		resp, err := makeResponse(w)
		if err != nil {
			return Response{}, err
		}

		return resp, nil
	}
}

func setHeader(event Request, req *http.Request) {
	for k, v := range event.Headers {
		if n := strings.Index(v, ","); n > 0 {
			for _, v := range strings.Split(v, ",") {
				req.Header.Add(k, v)
			}
			continue
		}
		req.Header.Set(k, v)
	}
	req.Header.Add("Cookie", strings.Join(event.Cookies, ";"))
}

func makeBody(body string, isBase64Encoded bool) (io.Reader, error) {
	switch {
	case body != "" && isBase64Encoded:
		data, err := base64.StdEncoding.DecodeString(body)
		if err != nil {
			return nil, fmt.Errorf("unable to base64 decode request body: %w", err)
		}
		return bytes.NewReader(data), nil

	case body != "":
		return strings.NewReader(body), nil

	default:
		return bytes.NewReader(nil), nil
	}
}

func makeV1Request(event Request) (*http.Request, error) {
	var encoded string
	if len(event.QueryStringParameters) > 0 {
		form := url.Values{}
		for k, v := range event.QueryStringParameters {
			form.Set(k, v)
		}
		encoded = "?" + form.Encode()
	}

	proto := event.Headers["x-forwarded-proto"]
	if proto == "" {
		proto = "http"
	}

	port := event.Headers["x-forwarded-port"]
	switch {
	case proto == "http" && port == "80":
		port = ""
	case proto == "https" && port == "443":
		port = ""
	}

	var uri string
	switch host := event.Headers["host"]; port {
	case "":
		uri = fmt.Sprintf("%v://%v", proto, host) + event.Path + encoded
	default:
		uri = fmt.Sprintf("%v://%v:%v", proto, host, port) + event.Path + encoded
	}

	body, err := makeBody(event.Body, event.IsBase64Encoded)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	req, err := http.NewRequest(event.HttpMethod, uri, body)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	var contentLength int64
	if s, ok := event.Headers["content-length"]; ok {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unable to parse content length, %v: %w", s, err)
		}
		contentLength = v
	}

	setHeader(event, req)

	req.ContentLength = contentLength
	req.RemoteAddr = event.Headers["x-forwarded-for"]
	req.RequestURI = event.Path

	return req, nil
}

func makeV2Request(event Request, prefix string) (*http.Request, error) {
	var uri string
	switch event.RawQueryString {
	case "":
		uri = "http://" + event.RequestContext.DomainName + stripPrefix(event.RawPath, prefix)
	default:
		uri = "http://" + event.RequestContext.DomainName + stripPrefix(event.RawPath, prefix) + "?" + event.RawQueryString
	}

	body, err := makeBody(event.Body, event.IsBase64Encoded)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	req, err := http.NewRequest(event.RequestContext.Http.Method, uri, body)
	if err != nil {
		return nil, fmt.Errorf("unable to create request: %w", err)
	}

	var contentLength int64
	if s, ok := event.Headers["content-length"]; ok {
		v, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("unable to parse content length, %v: %w", s, err)
		}
		contentLength = v
	}

	setHeader(event, req)

	req.ContentLength = contentLength
	req.RemoteAddr = event.Headers["x-forwarded-for"]
	req.RequestURI = stripPrefix(event.RawPath, prefix)

	return req, nil
}

func stripPrefix(path, prefix string) string {
	switch {
	case prefix == "":
		return path
	case strings.HasPrefix(path, prefix):
		return path[len(prefix):]
	default:
		return path
	}
}

func makeResponse(w *httptest.ResponseRecorder) (Response, error) {
	var body string
	if raw := w.Body.Bytes(); len(raw) > 0 {
		body = base64.StdEncoding.EncodeToString(raw)
	}

	headers := map[string]string{}
	for k, v := range w.Header() {
		switch len(v) {
		case 0:
			// ok
		case 1:
			headers[k] = v[0]
		default:
			headers[k] = strings.Join(v, ",")
		}
	}

	return Response{
		StatusCode:      w.Code,
		Headers:         headers,
		Body:            body,
		IsBase64Encoded: len(body) > 0,
	}, nil
}
