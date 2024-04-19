package deej

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
)

type httpSession struct {
	logger   *zap.SugaredLogger
	method   string
	url      string
	ch       chan float32
	lastVol  float32
	ctx      context.Context
	ctxClose context.CancelFunc
}

func httpParamsToKey(method, url string) string {
	return "http." + strings.ToUpper(method) + "." + url
}

func newHTTPSession(logger *zap.SugaredLogger, params []string) (*httpSession, error) {
	s := &httpSession{
		ch:      make(chan float32, 100),
		lastVol: -1,
	}

	if len(params) != 2 {
		return nil, fmt.Errorf("bad number of params (expected 2, got %v)", len(params))
	}

	s.method = strings.ToUpper(params[0])
	s.url = params[1]

	if !slices.Contains([]string{"GET", "POST", "PUT", "DELETE"}, s.method) {
		return nil, fmt.Errorf("bad http method: %v", s.method)
	}

	if _, err := url.Parse(s.url); err != nil {
		return nil, fmt.Errorf("bad http url: %w", err)
	}

	// use a self-identifying session name e.g. deej.sessions.chrome
	s.logger = logger.Named(s.Key())
	s.logger.Debugw(sessionCreationLogMessage, "session", s)

	s.ctx, s.ctxClose = context.WithCancel(context.Background())

	go s.loop()

	return s, nil
}

func (s *httpSession) loop() {
	for v := range s.ch {
	empty:
		// empty out the channel in case it buffered up
		for {
			select {
			case <-s.ctx.Done():
				return
			case v = <-s.ch:
				// nothing, loop and check again
			default:
				break empty
			}
		}

		s.logger.Debugw("sending http vol change", "vol", v)

		url := strings.ReplaceAll(s.url, "$VOL", strconv.FormatFloat(float64(v), 'f', -1, 32))
		req, err := http.NewRequestWithContext(s.ctx, s.method, url, nil)
		if err != nil {
			s.logger.Errorw("error making request", "err", err)
			return
		}

		client := &http.Client{
			Timeout: time.Second * 5,
		}
		resp, err := client.Do(req)
		if err != nil {
			// do not log context canceled
			if !errors.Is(err, context.Canceled) {
				s.logger.Errorw("error doing request", "err", err)
			}
			continue
		}
		resp.Body.Close()
	}
}

func (s *httpSession) GetVolume() float32 {
	return s.lastVol
}

func (s *httpSession) SetVolume(v float32) error {
	s.lastVol = v

	select {
	case s.ch <- v:
	default:
		return errors.New("http volume channel full")
	}

	return nil
}

func (s *httpSession) Key() string {
	return httpParamsToKey(s.method, s.url)
}

func (s *httpSession) Release() {
	s.ctxClose()
	close(s.ch)
}
