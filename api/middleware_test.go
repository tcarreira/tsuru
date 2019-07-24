// Copyright 2012 tsuru authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package api

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/codegangsta/negroni"
	"github.com/pkg/errors"
	"github.com/tsuru/config"
	"github.com/tsuru/tsuru/api/context"
	"github.com/tsuru/tsuru/app"
	"github.com/tsuru/tsuru/auth"
	"github.com/tsuru/tsuru/cmd"
	tsuruErrors "github.com/tsuru/tsuru/errors"
	"github.com/tsuru/tsuru/io"
	"github.com/tsuru/tsuru/servicemanager"
	appTypes "github.com/tsuru/tsuru/types/app"
	authTypes "github.com/tsuru/tsuru/types/auth"
	check "gopkg.in/check.v1"
)

type handlerLog struct {
	w        http.ResponseWriter
	r        *http.Request
	called   bool
	sleep    time.Duration
	response int
}

func doHandler() (http.HandlerFunc, *handlerLog) {
	h := &handlerLog{}
	return func(w http.ResponseWriter, r *http.Request) {
		if h.sleep != 0 {
			time.Sleep(h.sleep)
		}
		h.called = true
		h.w = w
		h.r = r
		if h.response != 0 {
			w.WriteHeader(h.response)
		}
	}, h
}

func (s *S) TestContextClearerMiddleware(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	context.AddRequestError(request, fmt.Errorf("Some Error"))
	h, log := doHandler()
	contextClearerMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	contErr := context.GetRequestError(request)
	c.Assert(contErr, check.IsNil)
}

func (s *S) TestFlushingWriterMiddleware(c *check.C) {
	tests := []struct {
		url             string
		config          map[string]time.Duration
		expectedLatency time.Duration
	}{
		{
			url:             "/",
			expectedLatency: 0,
		},
		{
			url:             "/",
			expectedLatency: 0,
			config:          map[string]time.Duration{},
		},
		{
			url:             "/?:mux-route-name=my-cool-handler",
			expectedLatency: 0,
			config:          map[string]time.Duration{},
		},
		{
			url:             "/?:mux-route-name=my-cool-handler",
			expectedLatency: 2 * time.Second,
			config: map[string]time.Duration{
				"my-cool-handler": 2 * time.Second,
			},
		},
	}
	for _, tt := range tests {
		flushingWriter := flushingWriterMiddleware{
			latencyConfig: tt.config,
		}
		recorder := httptest.NewRecorder()
		request, err := http.NewRequest("GET", tt.url, nil)
		c.Assert(err, check.IsNil)
		h, log := doHandler()
		flushingWriter.ServeHTTP(recorder, request, h)
		c.Assert(log.called, check.Equals, true)
		fWriter, ok := log.w.(*io.FlushingWriter)
		c.Assert(ok, check.Equals, true)
		c.Assert(fWriter.MaxLatency, check.Equals, tt.expectedLatency)
	}
}

func (s *S) TestSetRequestIDHeaderMiddleware(c *check.C) {
	config.Set("request-id-header", "Request-ID")
	defer config.Unset("request-id-header")
	rec := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	setRequestIDHeaderMiddleware(rec, req, h)
	c.Assert(log.called, check.Equals, true)
	reqID := context.GetRequestID(req, "Request-ID")
	c.Assert(reqID, check.Not(check.Equals), "")
}

func (s *S) TestSetRequestIDHeaderAlreadySet(c *check.C) {
	config.Set("request-id-header", "Request-ID")
	defer config.Unset("request-id-header")
	rec := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	req.Header.Set("Request-ID", "test")
	h, log := doHandler()
	setRequestIDHeaderMiddleware(rec, req, h)
	c.Assert(log.called, check.Equals, true)
	reqID := context.GetRequestID(req, "Request-ID")
	c.Assert(reqID, check.Equals, "test")
}

func (s *S) TestSetRequestIDHeaderMiddlewareNoConfig(c *check.C) {
	config.Unset("request-id-header")
	rec := httptest.NewRecorder()
	req, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	setRequestIDHeaderMiddleware(rec, req, h)
	c.Assert(log.called, check.Equals, true)
	reqID := context.GetRequestID(req, "")
	c.Assert(reqID, check.Equals, "")
}

func (s *S) TestSetVersionHeadersMiddleware(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	setVersionHeadersMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Header().Get("Supported-Tsuru"), check.Equals, tsuruMin)
	c.Assert(recorder.Header().Get("Supported-Crane"), check.Equals, craneMin)
	c.Assert(recorder.Header().Get("Supported-Tsuru-Admin"), check.Equals, tsuruAdminMin)
}

func (s *S) TestErrorHandlingMiddlewareWithoutError(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	errorHandlingMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Code, check.Equals, 200)
}

func (s *S) TestErrorHandlingMiddlewareWithError(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	context.AddRequestError(request, fmt.Errorf("something"))
	errorHandlingMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Code, check.Equals, 500)
}

func (s *S) TestErrorHandlingMiddlewareWithHTTPError(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	context.AddRequestError(request, &tsuruErrors.HTTP{Code: 403, Message: "other msg"})
	errorHandlingMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Code, check.Equals, 403)
}

func (s *S) TestErrorHandlingMiddlewareWithValidationError(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	context.AddRequestError(request, &tsuruErrors.ValidationError{Message: "invalid request"})
	errorHandlingMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Code, check.Equals, 400)
	c.Assert(recorder.Body.String(), check.DeepEquals, "invalid request\n")
}

func (s *S) TestErrorHandlingMiddlewareWithCauseValidationError(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	context.AddRequestError(request, errors.WithStack(&tsuruErrors.ValidationError{Message: "invalid request"}))
	errorHandlingMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Code, check.Equals, 400)
	c.Assert(recorder.Body.String(), check.DeepEquals, "invalid request\n")
}

func (s *S) TestErrorHandlingMiddlewareWithVerbosity(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	request.Header.Add(cmd.VerbosityHeader, "1")
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	context.AddRequestError(request, errors.WithStack(&tsuruErrors.ValidationError{Message: "invalid request"}))
	errorHandlingMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	c.Assert(recorder.Code, check.Equals, 400)
	c.Assert(strings.Contains(recorder.Body.String(), "github.com/tsuru/tsuru"), check.Equals, true)
}

func (s *S) TestAuthTokenMiddlewareWithoutToken(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t, check.IsNil)
}

func (s *S) TestAuthTokenMiddlewareWithToken(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer "+s.token.GetValue())
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t.GetValue(), check.Equals, s.token.GetValue())
	c.Assert(t.GetUserName(), check.Equals, s.token.GetUserName())
}

func (s *S) TestAuthTokenMiddlewareWithAPIToken(c *check.C) {
	user := auth.User{Email: "para@xmen.com", APIKey: "347r3487rh3489hr34897rh487hr0377rg308rg32"}
	err := s.conn.Users().Insert(&user)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer "+user.APIKey)
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t.GetValue(), check.Equals, user.APIKey)
	c.Assert(t.GetUserName(), check.Equals, user.Email)
}

func (s *S) TestAuthTokenMiddlewareWithTeamToken(c *check.C) {
	token, err := servicemanager.TeamToken.Create(authTypes.TeamTokenCreateArgs{
		Team: s.team.Name,
	}, s.token)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer "+token.Token)
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t, check.NotNil)
	c.Assert(t.GetValue(), check.Equals, token.Token)
	c.Assert(t.GetAppName(), check.Equals, "")
}

func (s *S) TestAuthTokenMiddlewareWithInvalidToken(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer ifyougotozah'ha'dumyoulldie")
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t, check.IsNil)
}

func (s *S) TestAuthTokenMiddlewareWithInvalidAPIToken(c *check.C) {
	user := auth.User{Email: "para@xmen.com", APIKey: "347r3487rh3489hr34897rh487hr0377rg308rg32"}
	err := s.conn.Users().Insert(&user)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer 12eh923d8ydh238eun`1po2ueh1`p2")
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t, check.IsNil)
}

func (s *S) TestAuthTokenMiddlewareUserTokenForApp(c *check.C) {
	a := app.App{Name: "something", Teams: []string{s.team.Name}}
	err := s.conn.Apps().Insert(a)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/?:app=something", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer "+s.token.GetValue())
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
	t := context.GetAuthToken(request)
	c.Assert(t.GetValue(), check.Equals, s.token.GetValue())
	c.Assert(t.GetUserName(), check.Equals, s.token.GetUserName())
}

func (s *S) TestAuthTokenMiddlewareUserTokenAppNotFound(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/?:app=something", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("Authorization", "bearer "+s.token.GetValue())
	h, log := doHandler()
	authTokenMiddleware(recorder, request, h)
	c.Assert(log.called, check.Equals, false)
	err = context.GetRequestError(request)
	c.Assert(err, check.NotNil)
	e, ok := err.(*tsuruErrors.HTTP)
	c.Assert(ok, check.Equals, true)
	c.Assert(e.Code, check.Equals, http.StatusNotFound)
}

func (s *S) TestRunDelayedHandlerWithoutHandler(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	runDelayedHandler(recorder, request)
}

func (s *S) TestRunDelayedHandlerWithHandler(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	context.SetDelayedHandler(request, h)
	runDelayedHandler(recorder, request)
	c.Assert(log.called, check.Equals, true)
}

func (s *S) TestAppLockMiddlewareDoesNothingWithoutApp(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
}

func (s *S) TestAppLockMiddlewareDoesNothingForGetRequests(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("GET", "/?:app=abc", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
}

func (s *S) TestAppLockMiddlewareReturns404IfNotApp(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/?:app=abc", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, h)
	c.Assert(log.called, check.Equals, false)
	httpErr := context.GetRequestError(request).(*tsuruErrors.HTTP)
	c.Assert(httpErr.Code, check.Equals, http.StatusNotFound)
	c.Assert(httpErr.Message, check.Equals, "App not found")
	request, err = http.NewRequest("POST", "/?:appname=abc", nil)
	c.Assert(err, check.IsNil)
	m.ServeHTTP(recorder, request, h)
	c.Assert(log.called, check.Equals, false)
	httpErr = context.GetRequestError(request).(*tsuruErrors.HTTP)
	c.Assert(httpErr.Code, check.Equals, http.StatusNotFound)
	c.Assert(httpErr.Message, check.Equals, "App not found")
}

func (s *S) TestAppLockMiddlewareOnLockedApp(c *check.C) {
	oldDuration := lockWaitDuration
	lockWaitDuration = time.Second
	defer func() { lockWaitDuration = oldDuration }()
	myApp := app.App{
		Name: "my-app",
		Lock: appTypes.AppLock{
			Locked:      true,
			Reason:      "/app/my-app/deploy",
			Owner:       "someone",
			AcquireDate: time.Date(2048, time.November, 10, 10, 0, 0, 0, time.UTC),
		},
	}
	err := s.conn.Apps().Insert(myApp)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/?:app=my-app", nil)
	c.Assert(err, check.IsNil)
	h, log := doHandler()
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, h)
	c.Assert(log.called, check.Equals, false)
	httpErr := context.GetRequestError(request).(*tsuruErrors.HTTP)
	c.Assert(httpErr.Code, check.Equals, http.StatusConflict)
	c.Assert(httpErr.Message, check.Matches, "App locked by someone, running /app/my-app/deploy. Acquired in 2048-11-10.*")
}

func (s *S) TestAppLockMiddlewareLocksAndUnlocks(c *check.C) {
	myApp := app.App{
		Name: "my-app",
	}
	err := s.conn.Apps().Insert(myApp)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/?:app=my-app", nil)
	c.Assert(err, check.IsNil)
	called := false
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, func(w http.ResponseWriter, r *http.Request) {
		a, appErr := app.GetByName(request.URL.Query().Get(":app"))
		c.Assert(appErr, check.IsNil)
		c.Assert(a.Lock.Locked, check.Equals, true)
		called = true
	})
	c.Assert(called, check.Equals, true)
	a, err := app.GetByName(request.URL.Query().Get(":app"))
	c.Assert(err, check.IsNil)
	c.Assert(a.Lock.Locked, check.Equals, false)
}

func (s *S) TestAppLockMiddlewareWithPreventUnlock(c *check.C) {
	myApp := app.App{
		Name: "my-app",
	}
	err := s.conn.Apps().Insert(myApp)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/?:app=my-app", nil)
	c.Assert(err, check.IsNil)
	called := false
	context.SetPreventUnlock(request)
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, func(w http.ResponseWriter, r *http.Request) {
		a, appErr := app.GetByName(request.URL.Query().Get(":app"))
		c.Assert(appErr, check.IsNil)
		c.Assert(a.Lock.Locked, check.Equals, true)
		called = true
	})
	c.Assert(called, check.Equals, true)
	a, err := app.GetByName(request.URL.Query().Get(":app"))
	c.Assert(err, check.IsNil)
	c.Assert(a.Lock.Locked, check.Equals, true)
}

func (s *S) TestAppLockMiddlewareDoesNothingForExcludedHandlers(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/?:app=abc", nil)
	c.Assert(err, check.IsNil)
	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})
	context.SetDelayedHandler(request, finalHandler)
	h, log := doHandler()
	m := &appLockMiddleware{
		excludedHandlers: []http.Handler{finalHandler},
	}
	m.ServeHTTP(recorder, request, h)
	c.Assert(log.called, check.Equals, true)
}

func (s *S) TestAppLockMiddlewareWaitForLock(c *check.C) {
	myApp := app.App{
		Name: "my-app",
		Lock: appTypes.AppLock{
			Locked:      true,
			Reason:      "/app/my-app/deploy",
			Owner:       "someone",
			AcquireDate: time.Date(2048, time.November, 10, 10, 0, 0, 0, time.UTC),
		},
	}
	err := s.conn.Apps().Insert(myApp)
	c.Assert(err, check.IsNil)
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("POST", "/?:app=my-app", nil)
	c.Assert(err, check.IsNil)
	called := false
	wg := sync.WaitGroup{}
	wg.Add(1)
	defer wg.Wait()
	go func() {
		defer wg.Done()
		time.Sleep(time.Second)
		app.ReleaseApplicationLock(myApp.Name)
	}()
	m := &appLockMiddleware{}
	m.ServeHTTP(recorder, request, func(w http.ResponseWriter, r *http.Request) {
		a, appErr := app.GetByName(request.URL.Query().Get(":app"))
		c.Assert(appErr, check.IsNil)
		c.Assert(a.Lock.Locked, check.Equals, true)
		called = true
	})
	c.Assert(called, check.Equals, true)
	a, err := app.GetByName(request.URL.Query().Get(":app"))
	c.Assert(err, check.IsNil)
	c.Assert(a.Lock.Locked, check.Equals, false)
}

func (s *S) TestLoggerMiddleware(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("PUT", "/my/path", nil)
	c.Assert(err, check.IsNil)
	request.Header.Set("User-Agent", "ardata 1.1")
	h, handlerLog := doHandler()
	handlerLog.sleep = 100 * time.Millisecond
	handlerLog.response = http.StatusOK
	var out bytes.Buffer
	middle := loggerMiddleware{
		logger: log.New(&out, "", 0),
	}
	middle.ServeHTTP(negroni.NewResponseWriter(recorder), request, h)
	c.Assert(handlerLog.called, check.Equals, true)
	timePart := time.Now().Format(time.RFC3339Nano)[:19]
	c.Assert(out.String(), check.Matches, fmt.Sprintf(`%s\..+? http PUT /my/path 200 "ardata 1.1" in 1\d{2}\.\d+ms`+"\n", timePart))
}

func (s *S) TestLoggerMiddlewareWithoutStatusCode(c *check.C) {
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("PUT", "/my/path", nil)
	c.Assert(err, check.IsNil)
	h, handlerLog := doHandler()
	handlerLog.sleep = 100 * time.Millisecond
	handlerLog.response = 0
	var out bytes.Buffer
	middle := loggerMiddleware{
		logger: log.New(&out, "", 0),
	}
	middle.ServeHTTP(negroni.NewResponseWriter(recorder), request, h)
	c.Assert(handlerLog.called, check.Equals, true)
	timePart := time.Now().Format(time.RFC3339Nano)[:19]
	c.Assert(out.String(), check.Matches, fmt.Sprintf(`%s\..+? http PUT /my/path 200 "" in 1\d{2}\.\d+ms`+"\n", timePart))
}

func (s *S) TestLoggerMiddlewareWithRequestID(c *check.C) {
	config.Set("request-id-header", "Request-ID")
	defer config.Unset("request-id-header")
	recorder := httptest.NewRecorder()
	request, err := http.NewRequest("PUT", "/my/path", nil)
	c.Assert(err, check.IsNil)
	context.SetRequestID(request, "Request-ID", "my-rid")
	h, handlerLog := doHandler()
	handlerLog.sleep = 100 * time.Millisecond
	handlerLog.response = http.StatusOK
	var out bytes.Buffer
	middle := loggerMiddleware{
		logger: log.New(&out, "", 0),
	}
	middle.ServeHTTP(negroni.NewResponseWriter(recorder), request, h)
	c.Assert(handlerLog.called, check.Equals, true)
	timePart := time.Now().Format(time.RFC3339Nano)[:19]
	c.Assert(out.String(), check.Matches, fmt.Sprintf(`%s\..+? http PUT /my/path 200 "" in 1\d{2}\.\d+ms \[Request-ID: my-rid\]`+"\n", timePart))
}

func (s *S) TestLoggerMiddlewareHTTPS(c *check.C) {
	h, handlerLog := doHandler()
	handlerLog.response = http.StatusOK
	var out bytes.Buffer
	middle := loggerMiddleware{
		logger: log.New(&out, "", 0),
	}
	n := negroni.New()
	n.Use(&middle)
	n.UseHandler(h)
	srv := httptest.NewTLSServer(n)
	defer srv.Close()
	cli := srv.Client()
	request, err := http.NewRequest("PUT", srv.URL+"/my/path", nil)
	c.Assert(err, check.IsNil)
	rsp, err := cli.Do(request)
	c.Assert(err, check.IsNil)
	c.Assert(rsp.StatusCode, check.Equals, http.StatusOK)
	c.Assert(handlerLog.called, check.Equals, true)
	timePart := time.Now().Format(time.RFC3339Nano)[:19]
	c.Assert(out.String(), check.Matches, fmt.Sprintf(`%s\..+? https PUT /my/path 200 "Go-http-client/1.1" in \d{1}\.\d+ms`+"\n", timePart))
}

func (s *S) TestInputValues(c *check.C) {
	tests := []struct {
		body        string
		field       string
		contentType string
		expected    []string
		present     bool
		qs          string
	}{
		{
			body:        "",
			field:       "",
			contentType: "application/json",
		},
		{
			body:        "",
			field:       "",
			contentType: "application/x-www-form-urlencoded",
		},
		{
			body:        `{"foo": "bar"}`,
			field:       "foo",
			contentType: "application/x-www-form-urlencoded",
		},
		{
			body:        `{"foo": "bar"}`,
			field:       "foo",
			contentType: "application/json",
			present:     true,
			expected:    []string{"bar"},
		},
		{
			body:        `{"foo": ["bar", "baz"]}`,
			field:       "foo",
			contentType: "application/json",
			present:     true,
			expected:    []string{"bar", "baz"},
		},
		{
			body:        `{"foo": []}`,
			field:       "foo",
			contentType: "application/json",
			present:     true,
		},
		{
			body:        `{"foo": ""}`,
			field:       "foo",
			contentType: "application/json",
			present:     true,
			expected:    []string{""},
		},
		{
			body:        `foo=bar`,
			field:       "foo",
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    []string{"bar"},
		},
		{
			body:        `foo=bar&foo=baz`,
			field:       "foo",
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    []string{"bar", "baz"},
		},
		{
			body:        `foo=`,
			field:       "foo",
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    []string{""},
		},
		{
			field:       "foo",
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    []string{"x"},
			qs:          "foo=x",
		},
		{
			body:        `foo=a`,
			field:       "foo",
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    []string{"a", "x"},
			qs:          "foo=x",
		},
		{
			field:    "foo",
			present:  true,
			expected: []string{"x"},
			qs:       "foo=x",
		},
		{
			body:        `{"foo": "a"}`,
			field:       "foo",
			contentType: "application/json",
			present:     true,
			expected:    []string{"a", "x"},
			qs:          "foo=x",
		},
	}
	for i, tt := range tests {
		c.Logf("test %d: %#v", i, tt)
		body := strings.NewReader(tt.body)
		request, err := http.NewRequest("POST", "/my/path?"+tt.qs, body)
		c.Assert(err, check.IsNil)
		request.Header.Set("Content-Type", tt.contentType)
		values, ok := InputValues(request, tt.field)
		c.Check(ok, check.Equals, tt.present)
		sort.Strings(values)
		sort.Strings(tt.expected)
		c.Check(values, check.DeepEquals, tt.expected)
	}
}

func (s *S) TestInputFields(c *check.C) {
	tests := []struct {
		body        string
		contentType string
		expected    url.Values
		present     bool
		qs          string
	}{
		{
			body:        "",
			contentType: "application/json",
		},
		{
			body:        "",
			contentType: "application/x-www-form-urlencoded",
			expected:    url.Values{},
		},
		{
			body:        `{"foo": "bar"}`,
			contentType: "application/x-www-form-urlencoded",
			expected:    url.Values{`{"foo": "bar"}`: {""}},
		},
		{
			body:        `{"foo": "bar"}`,
			contentType: "application/json",
			present:     true,
			expected:    url.Values{"foo": {"bar"}},
		},
		{
			body:        `{"foo": ["bar", "baz"]}`,
			contentType: "application/json",
			present:     true,
			expected:    url.Values{"foo.0": {"bar"}, "foo.1": {"baz"}},
		},
		{
			body:        `{"foo": []}`,
			contentType: "application/json",
			present:     true,
			expected:    url.Values{"foo": {""}},
		},
		{
			body:        `{"foo": ""}`,
			contentType: "application/json",
			present:     true,
			expected:    url.Values{"foo": {""}},
		},
		{
			body:        `foo=bar`,
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    url.Values{"foo": {"bar"}},
		},
		{
			body:        `foo=bar&foo=baz`,
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    url.Values{"foo": {"bar", "baz"}},
		},
		{
			body:        `foo=`,
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    url.Values{"foo": {""}},
		},
		{
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    url.Values{"foo": {"x"}},
			qs:          "foo=x",
		},
		{
			body:        `foo=a`,
			contentType: "application/x-www-form-urlencoded",
			present:     true,
			expected:    url.Values{"foo": {"a", "x"}},
			qs:          "foo=x",
		},
		{
			present:  true,
			expected: url.Values{"foo": {"x"}},
			qs:       "foo=x",
		},
		{
			body:        `{"foo": "a"}`,
			contentType: "application/json",
			present:     true,
			expected:    url.Values{"foo": {"x", "a"}},
			qs:          "foo=x",
		},
	}
	for i, tt := range tests {
		c.Logf("test %d: %#v", i, tt)
		body := strings.NewReader(tt.body)
		request, err := http.NewRequest("POST", "/my/path?"+tt.qs, body)
		c.Assert(err, check.IsNil)
		request.Header.Set("Content-Type", tt.contentType)
		values := InputFields(request)
		c.Check(values, check.DeepEquals, tt.expected)
	}
}
