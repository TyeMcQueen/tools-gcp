package trace_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-lager/gcp-spans"
	"github.com/TyeMcQueen/go-tutl"
	"github.com/TyeMcQueen/tools-gcp/trace"
)

func TestTrace(t *testing.T) {
	u := tutl.New(t)
	logs := &bytes.Buffer{}
	defer u.Is("", logs.Bytes(), "no final errors")
	defer lager.SetOutput(logs)()

	proj := os.Getenv("GCP_PROJECT_ID")
	if "" == os.Getenv("GCP_PROJECT_ID") {
		out, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0666)
		if nil != err {
			u.Log("Can't write to /dev/tty: ", err)
			out = os.Stderr
		}
		fmt.Fprintf(out, "SKIPPED: %s.\n",
			"Testing requires GCP_PROJECT_ID set and default creds")
		t.SkipNow()
		return
	}

	ctx := context.Background()
	var spanReg *trace.Registrar
	defer trace.StartServer(&ctx, 1, &spanReg)()
	empty := spans.ContextGetSpan(ctx)
	u.IsNot(nil, empty, "NewFactory")

	u.Is(proj, empty.GetProjectID(), "empty GetProjectID")
	u.Is("", empty.GetTraceID(), "empty GetTraceID")
	u.Is(0, empty.GetSpanID(), "empty GetSpanID")
	u.Is(time.Time{}, empty.GetStart(), "empty GetStart")
	u.Is("", empty.GetTracePath(), "empty GetTracePath")
	u.Is("", empty.GetSpanPath(), "empty GetSpanPath")
	u.Is("", empty.GetCloudContext(), "empty GetCloudContext")
	u.Is("", logs.Bytes(), "logs nothing 1")
	logs.Reset()

	u.Is(nil, empty.NewSubSpan(), "empty NewSubSpan")
	u.Like(logs.Bytes(), "empty NewSubSpan logs",
		"*disallowed method", "*empty span", `"_stack":`)
	logs.Reset()

	u.Is(time.Duration(0), empty.Finish(), "empty Finish")
	u.Like(logs.Bytes(), "empty Finish logs",
		"*disallowed method", "*empty span", `"_stack":`)
	logs.Reset()

	fakeHead := make(http.Header)
	empty.SetHeader(fakeHead)
	u.Is(0, len(fakeHead), "empty SetHeader is no-op")

	sp := empty.NewSpan()
	sp.SetHeader(fakeHead)
	inHead := sp
	u.Is(sp.GetCloudContext(), fakeHead.Get(spans.TraceHeader),
		"SetHeader sets "+spans.TraceHeader)
	u.Is("", logs.Bytes(), "logs nothing 2")
	logs.Reset()
	if u.IsNot(nil, sp, "empty NewSpan") {
		u.IsNot(0, sp.GetSpanID(), "empty NewTrace not empty")
	}

	u.Log(sp.GetTraceID())
	sp.Finish()
	spanReg.WaitForIdleRunners()
	u.Is("", logs.Bytes(), "no errors finishing 1")
	logs.Reset()

	sp = sp.NewTrace()
	if u.IsNot(nil, sp, "NewTrace") {
		u.IsNot(0, sp.GetSpanID(), "NewTrace not empty")
	}
	time.Sleep(time.Second/10)

	sub := sp.NewSpan()
	u.Is(sp.GetTraceID(), sub.GetTraceID(), "NewSpan() preserves trace")
	sub.SetIsPublisher()
	sub.SetIsSubscriber()
	sub.SetIsClient()
	sub.SetDisplayName("fetch user")
	sub.AddAttribute("url", "https://example.com/api/users")
	sub.AddAttribute("response_bytes", 407)
	sub.SetStatusCode(200)
	sub.SetStatusMessage("OK")
	time.Sleep(time.Second/10)
	sub.Finish()

	time.Sleep(time.Second/10)
	sp.SetIsServer()
	sp.SetStatusMessage("Done")
	sp.AddAttribute("req_bytes", int64(-1))
	sp.AddAttribute("isAnonymous", true)
	u.Like(sp.AddAttribute("wrongType", 0.0), "AddAttribute float",
		"AddAttribute", "*invalid value type", "*(float64)")
	u.Log(sp.GetTraceID())
	sp.Finish()
	spanReg.WaitForIdleRunners()
	u.Is("", logs.Bytes(), "no errors finishing 2")
	logs.Reset()

	u.Is(nil, empty.AddAttribute("key", "value"), "empty AddAttribute")
	u.Like(logs.Bytes(), "empty AddAttribute logs",
		"*disallowed method", "*empty span", `"_stack":`)
	logs.Reset()

	im, err := sp.Import("non-valid", 2)
	u.Is(nil, im, "invalid Import")
	u.Like(err, "invalid Import error",
		"*import()", "*invalid trace id", "*(non-valid)")

	im, err = sp.Import(sp.GetTraceID(), sp.GetSpanID())
	u.IsNot(nil, im, "valid Import")
	u.Is(nil, err, "valid Import error")

	im = sub.ImportFromHeaders(fakeHead)
	u.Is(inHead.GetTraceID(), im.GetTraceID(), "imported trace ID")
	u.Is(inHead.GetSpanID(), im.GetSpanID(), "imported span ID")

	for m, f := range map[string]func(){
		"SetDisplayName":   func() { im.SetDisplayName("") },
		"SetIsClient":      func() { im.SetIsClient() },
		"SetIsServer":      func() { im.SetIsServer() },
		"SetIsPublisher":   func() { im.SetIsPublisher() },
		"SetIsSubscriber":  func() { im.SetIsSubscriber() },
		"SetStatusCode":    func() { im.SetStatusCode(0) },
		"SetStatusMessage": func() { im.SetStatusMessage("") },
	} {
		f()
		u.Like(logs.Bytes(), "empty "+m+" logs",
			"*disallowed method", "*import()ed span", `"_stack":`)
		logs.Reset()
	}

	spanReg.Halt()
}
