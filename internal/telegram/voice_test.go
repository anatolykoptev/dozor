package telegram

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const fakeBotToken = "123456:SECRETbotTOKENvalue"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// scenario is a fake Telegram transport under the REAL tgbotapi + tgsafe client
// chain: getMe/getFile/download/getUpdates answer or fail as configured.
type scenario struct {
	getFileErr, downloadErr, getUpdatesErr error
	downloadStatus                         int
}

func (s scenario) RoundTrip(r *http.Request) (*http.Response, error) {
	ok := func(body string) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
	}
	p := r.URL.Path
	switch {
	case strings.HasSuffix(p, "/getMe"):
		return ok(`{"ok":true,"result":{"id":1,"is_bot":true,"first_name":"b","username":"b"}}`)
	case strings.HasSuffix(p, "/getFile"):
		if s.getFileErr != nil {
			return nil, s.getFileErr
		}
		return ok(`{"ok":true,"result":{"file_id":"f1","file_path":"voice/a.oga"}}`)
	case strings.HasSuffix(p, "/getUpdates"):
		if s.getUpdatesErr != nil {
			return nil, s.getUpdatesErr
		}
		return ok(`{"ok":true,"result":[]}`)
	case strings.Contains(p, "/file/"):
		if s.downloadErr != nil {
			return nil, s.downloadErr
		}
		return &http.Response{StatusCode: s.downloadStatus, Body: http.NoBody, Header: http.Header{}, Request: r}, nil
	}
	return nil, errors.New("unexpected request")
}

const fakeAPI = "https://api.telegram.org/bot%s/%s"

// newVoiceChannel builds the channel through the production constructor path.
func newVoiceChannel(t *testing.T, s scenario) *Channel {
	t.Helper()
	bot, dl, err := newBot(fakeBotToken, fakeAPI, &http.Client{Transport: s})
	if err != nil {
		t.Fatal(err)
	}
	return &Channel{bot: bot, dl: dl, sttURL: "http://127.0.0.1:1", sttLang: "en"}
}

func voiceMsg() *tgbotapi.Voice { return &tgbotapi.Voice{FileID: "f1", FileSize: 9} }

// logAsBotDoes renders err the way handleMessage does when transcription fails.
func logAsBotDoes(err error) string {
	var buf bytes.Buffer
	l := slog.New(slog.NewTextHandler(&buf, nil))
	l.Error("telegram: voice transcription failed", slog.Any("error", err))
	return buf.String()
}

func TestTranscribeVoiceErrorsNeverContainBotToken(t *testing.T) {
	cases := []struct {
		name string
		s    scenario
		want string
	}{
		{"download transport failure", scenario{downloadErr: errors.New("simulated transport failure")}, "simulated transport failure"},
		{"download HTTP 404", scenario{downloadStatus: http.StatusNotFound}, "404"},
		{"getFile transport failure", scenario{getFileErr: errors.New("simulated getFile failure")}, "simulated getFile failure"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := newVoiceChannel(t, c.s)
			_, err := ch.transcribeVoice(context.Background(), voiceMsg())
			assertNoToken(t, err)
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("cause lost: %v", err)
			}
		})
	}
	// Control: the same failing transport under a plain client (what NewBotAPI
	// did) leaks the token, so the cases above prove the scrubbing.
	_, leaky := tgbotapi.NewBotAPIWithClient(fakeBotToken, fakeAPI, &http.Client{Transport: roundTripFunc(
		func(*http.Request) (*http.Response, error) { return nil, errors.New("simulated") })})
	if leaky == nil || !strings.Contains(leaky.Error(), "SECRET") {
		t.Fatalf("control: the plain client should leak the token, got %v", leaky)
	}
}

func assertNoToken(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error contains the bot token: %v", err)
	}
	if logged := logAsBotDoes(err); strings.Contains(logged, "SECRET") {
		t.Errorf("log line contains the bot token: %s", logged)
	}
}

// The real GetUpdatesChan failing poll goes through the SDK package logger.
func TestStartUpdatesFailedPollIsScrubbed(t *testing.T) {
	ch := newVoiceChannel(t, scenario{getUpdatesErr: errors.New("simulated network blip")})
	var out syncBuf
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, nil)))
	t.Cleanup(func() {
		slog.SetDefault(old)
		_ = tgbotapi.SetLogger(stdlog.New(os.Stderr, "", stdlog.LstdFlags))
	})

	updates := ch.startUpdates()
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Failed to get updates") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	ch.bot.StopReceivingUpdates()
	for range updates { // closed when the poll goroutine exits; only then is the global logger safe to restore
	}
	got := out.String()
	if !strings.Contains(got, "Failed to get updates") {
		t.Fatalf("the SDK never logged a failed poll: %q", got)
	}
	if strings.Contains(got, "SECRET") || strings.Contains(got, fakeBotToken) {
		t.Errorf("SDK log line contains the bot token: %s", got)
	}
}

type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestTranscribeVoiceLogsLengthNotTranscript(t *testing.T) {
	const transcript = "private spoken words 4471"
	sttSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"text":%q,"duration":2.5}`, transcript)
	}))
	defer sttSrv.Close()
	b := newVoiceChannel(t, scenario{downloadStatus: http.StatusOK})
	b.sttURL = sttSrv.URL

	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	text, err := b.transcribeVoice(context.Background(), voiceMsg())
	if err != nil || text != transcript {
		t.Fatalf("text=%q err=%v", text, err)
	}
	log := buf.String()
	if strings.Contains(log, "private") || strings.Contains(log, "4471") {
		t.Errorf("transcript logged: %s", log)
	}
	if !strings.Contains(log, "text_runes=25") {
		t.Errorf("length not logged: %s", log)
	}
}
