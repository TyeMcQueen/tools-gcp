package trace

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-lager/gcp-spans"
	"github.com/TyeMcQueen/go-tutl"
)

func TestTrace(t *testing.T) {
	u := tutl.New(t)
	logs := &bytes.Buffer{}
	defer u.Is("", logs.Bytes(), "no final errors")
	defer lager.SetOutput(logs)()

	proj := os.Getenv("GCP_PROJECT_ID")
	if "" == proj {
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

	var ctx context.Context

	_ = lager.ExitViaPanic()

	os.Unsetenv("GCP_PROJECT_ID")
	ctx = nil
	ex := u.GetPanic(func(){
		StartServer(&ctx, 1)
	})
	u.IsNot(nil, ex, "Start fails w/o proj")
	u.Like(logs.Bytes(), "Start no proj logs",
		"[cC]ould not start Registrar")
	logs.Reset()
	os.Setenv("GCP_PROJECT_ID", proj)

	os.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "/no-creds")
	ctx = context.Background()
	ex = u.GetPanic(func(){
		StartServer(&ctx, 1)
	})
	u.IsNot(nil, ex, "Start fails w/o creds")
	u.Like(logs.Bytes(), "Start no creds logs",
		"[fF]ailed to create CloudTrace client")
	logs.Reset()
	os.Unsetenv("GOOGLE_APPLICATION_CREDENTIALS")

	_, err := NewClient(nil, nil)
	u.Is(nil, err, "NewClient(nil, nil) works")

	ctx = context.Background()
	var spanReg *Registrar
	os.Setenv("SPAN_QUEUE_CAPACITY", "2")
	defer StartServer(&ctx, 1, &spanReg)()
	empty := spans.ContextGetSpan(ctx)
	u.IsNot(nil, empty, "NewFactory")

	// Test method calls on an empty Factory

	u.Is(proj, empty.GetProjectID(), "empty GetProjectID")
	u.Is("", empty.GetTraceID(), "empty GetTraceID")
	u.Is(0, empty.GetSpanID(), "empty GetSpanID")
	u.Is(time.Time{}, empty.GetStart(), "empty GetStart")
	u.Is("", empty.GetTracePath(), "empty GetTracePath")
	u.Is("", empty.GetSpanPath(), "empty GetSpanPath")
	u.Is("", empty.GetCloudContext(), "empty GetCloudContext")
	u.Is(-time.Second, empty.GetDuration(), "empty GetDuration")
	u.Is("", logs.Bytes(), "logs nothing 1")
	logs.Reset()

	u.Is(spans.ROSpan{}, empty.NewSubSpan(), "empty NewSubSpan")
	u.Like(logs.Bytes(), "empty NewSubSpan logs",
		"*disallowed method", "*empty span", `"_stack":`)
	logs.Reset()

	u.Is(time.Duration(0), empty.Finish(), "empty Finish")
	u.Like(logs.Bytes(), "empty Finish logs",
		"*disallowed method", "*empty span", `"_stack":`)
	logs.Reset()

	// Test importing trace from headers

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

	u.Log("trace ID: ", sp.GetTraceID()) // So you can view spans in GCP
	u.Is(-time.Second, sp.GetDuration(), "GetDuration before")
	sp.SetDisplayName("mistake").SetDisplayName("")
	dur := sp.Finish()
	u.Is(true, 0 <= dur, u.S("Finish() duration should not be negative: ", dur))
	u.Is(os.Args[0], sp.(*Span).details.DisplayName.Value, "name not blank")
	spanReg.WaitForIdleRunners()
	u.Is("", logs.Bytes(), "no errors finishing 1")
	logs.Reset()

	u.Is(time.Duration(0), sp.Finish(), "2nd Finish()")
	u.Like(logs.Bytes(), "2nd Finish logs",
		"Disallowed method", "Finish[(][)]ed")
	logs.Reset()

	u.Is(dur, sp.GetDuration(), "GetDuration after")
	u.Is("", logs.Bytes(), "GetDuration after logs")
	logs.Reset()

	// Test creating our own trace

	sp = sp.NewTrace()
	if u.IsNot(nil, sp, "NewTrace") {
		u.IsNot(0, sp.GetSpanID(), "NewTrace not empty")
	}
	time.Sleep(time.Second / 10)

	sub := sp.NewSpan()
	u.Is(sp.GetTraceID(), sub.GetTraceID(), "NewSpan() preserves trace")
	sub.SetIsPublisher()
	sub.SetIsSubscriber()
	sub.SetIsClient()
	sub.SetDisplayName("fetch user")
	sub.AddPairs("bool", false, "int", 0, "int64", int64(0), "nil", nil,
		"dur", time.Second, "err", io.EOF)
	attrs := sub.(*Span).details.Attributes.AttributeMap
	if !u.Is(2, len(attrs), "keys after AddPairs()") {
		u.Log("Attributes: ", attrs)
	}
	u.Is("1s", attrs["dur"].StringValue.Value, "dur atttrib")
	u.Is("EOF", attrs["err"].StringValue.Value, "err atttrib")
	u.Is(nil,
		sub.AddAttribute("url", "https://example.com/api/users"),
		"AddAttrib url err")
	u.Like(sub.AddAttribute("", "value"), "blank key err",
		"AddAttribute[(][)]", "'key' must not be empty string")
	u.Is(nil, sub.AddAttribute("response_bytes", 407), "AddAttrib bytes err")
	sub.SetStatusCode(404)
	sub.SetStatusMessage("Not found")

	// Simulate sp.kidSpan wrapping through 0:
	{
		s := sp.(*Span)
		s.kidSpan = -s.spanInc
		subspan := sp.NewSubSpan()
		u.Is(s.spanInc, subspan.GetSpanID(), "SubSpan skips 0")
	}

	// Test AddPairs() failures on valid span:
	u.Is("", logs.Bytes(), "no errors 3")
	logs.Reset()
	sub.AddPairs("justkey")
	u.Like(logs.Bytes(), "justkey logs",
		"*ignoring", "*unpaired", "*last arg", "AddPairs[(][)]",
		`"arg":"justkey"`)
	logs.Reset()
	sub.AddPairs(10, "value")
	u.Like(logs.Bytes(), "non-string key logs",
		"*non-string key", `"key":10,`, `"type":"int",`)
	logs.Reset()
	sub.AddPairs("unsup", 1.0)
	u.Like(logs.Bytes(), "wrong attrib type logs",
		"*invalid value type", "[(]float64[)]", `"key":"unsup"`, `"val":1`)
	logs.Reset()

	time.Sleep(time.Second / 10)
	sub.Finish()
	u.Is("fetch user", sub.(*Span).details.DisplayName.Value, "name kept")

	time.Sleep(time.Second / 10)
	sp.SetIsServer()
	sp.SetStatusMessage("Rejected")
	sp.AddAttribute("req_bytes", int64(-1))
	sp.AddAttribute("isAnonymous", true)
	u.Like(sp.AddAttribute("wrongType", 0.0), "AddAttribute float",
		"AddAttribute", "*invalid value type", "*(float64)")
	u.Log("trace ID: ", sp.GetTraceID())

	// Finish when queue full
	{
		readys := make(chan Span, 0)
		empty := Span{ch: readys}
		for i := spanReg.runners; 0 < i; i-- {
			spanReg.queue <- empty
		}
		spanReg.queue <- empty
		spanReg.queue <- empty
		sp.Finish()
		for i := spanReg.runners; 0 < i; i-- {
			<-readys
		}
		<-readys
		<-readys
	}
	spanReg.WaitForIdleRunners()
	u.Is("", logs.Bytes(), "no errors finishing 2")
	logs.Reset()

	// Test "Push" functions:

	span := spans.ContextGetSpan(ctx)
	if !u.IsNot(nil, span, "starting context has factory") {
		t.FailNow()
	}
	u.Is(0, span.GetSpanID(), "context starts with empty factory")

	ctx2, pushed := ContextPushSpan(ctx, "pushed")
	u.Is(true, pushed == spans.ContextGetSpan(ctx2), "got pushed")
	u.Is("pushed", pushed.(*Span).details.DisplayName.Value, "pushed name")

	req, err := http.NewRequestWithContext(
		ctx, "GET", "http://localhost/foo", nil)

	// Test our method for detecting deep copies:
	req.Header.Set("x-deep-copy", "true")
	req2 := req.WithContext(ctx)
	req.Header.Set("x-deep-copy", "false")
	u.Is("false", req2.Header.Get("x-deep-copy"), "can detect deep copy")

	req.Header.Set("x-deep-copy", "true")
	u.Is(nil, err, "err from http.NewRequest")
	bg := context.Background()
	req2, ctx2, pushed = RequestPushSpan(req, bg, "req2")
	req.Header.Set("x-deep-copy", "false")
	u.Is("true", req2.Header.Get("x-deep-copy"), "failure deep-copies req")
	req.Header.Set("x-deep-copy", "true")
	u.Is(true, bg == ctx2, "req failure returns orig context")
	u.Is(true, bg == req2.Context(), "req failure preserves context")
	u.Like(logs.Bytes(), "req undecorated logs",
		"RequestPushSpan[(][)]", "[uU]ndecorated Context", `"_stack":`)
	logs.Reset()

	req2, ctx2, pushed = RequestPushSpan(req, nil, "req2")
	u.Is(true, pushed == spans.ContextGetSpan(ctx2), "got pushed req2")
	u.Is(true, ctx2 == req2.Context(), "req2 got ctx2")
	req.Header.Set("x-deep-copy", "false")
	u.Is("true", req2.Header.Get("x-deep-copy"), "req2 is deep copy")
	req.Header.Set("x-deep-copy", "true")

	ctx2 = ctx
	pushed = PushSpan(nil, &ctx2, "push1")
	u.Is(true, ctx2 != ctx, "Push1 changed ctx1")
	u.Is(true, pushed == spans.ContextGetSpan(ctx2), "got push1")
	u.Is("push1", pushed.(*Span).details.DisplayName.Value, "push1 name")

	req2, ctx2 = req, ctx
	pushed = PushSpan(&req2, &ctx2, "push2")
	u.Is(true, req2 != req, "Push2 changed req")
	u.Is(true, ctx2 != ctx, "Push2 changed ctx")
	u.Is(true, ctx2 == req2.Context(), "Push2 req has ctx")
	u.Is(true, pushed == spans.ContextGetSpan(ctx2), "got push2")
	u.Is("push2", pushed.(*Span).details.DisplayName.Value, "push2 name")
	req.Header.Set("x-deep-copy", "false")
	u.Is("false", req2.Header.Get("x-deep-copy"), "Push2 req not deep copy")
	req.Header.Set("x-deep-copy", "true")

	req2, ctx2 = req, nil
	pushed = PushSpan(&req2, &ctx2, "push3")
	u.Is(true, req2 != req, "Push3 changed req")
	u.Is(true, ctx2 != ctx, "Push3 changed ctx")
	u.Is(true, ctx2 == req2.Context(), "Push3 req has ctx")
	u.Is(true, pushed == spans.ContextGetSpan(ctx2), "got push3")
	u.Is("push3", pushed.(*Span).details.DisplayName.Value, "push3 name")
	req.Header.Set("x-deep-copy", "false")
	u.Is("false", req2.Header.Get("x-deep-copy"), "Push3 req not deep copy")
	req.Header.Set("x-deep-copy", "true")

	u.Is("", logs.Bytes(), "no errors pushing")
	logs.Reset()

	// Test misc "Push" errors:

	ctx2, span = ContextPushSpan(nil, "n/a")
	u.Like(logs.Bytes(), "ContextPush nil logs",
		"ContextPushSpan[(][)]", "passed nil Context")
	logs.Reset()
	ctx2, span = ContextPushSpan(context.Background(), "n/a")
	u.Like(logs.Bytes(), "ContextPush undecorated logs",
		"ContextPushSpan[(][)]", "passed undecorated Context")
	logs.Reset()

	req2, ctx2 = nil, bg
	pushed = PushSpan(&req2, &ctx2, "push4")
	u.Is(0, pushed.GetSpanID(), "push4 span empty")
	u.Is(nil, req2, "Push4 req stays nil")
	u.Is(true, ctx2 == bg, "Push4 ctx unchanged")
	u.Like(logs.Bytes(), "Push4 logs",
		"PushSpan[(][)]", "passed undecorated Context")
	logs.Reset()

	req2, ctx2 = nil, nil
	pushed = PushSpan(&req2, &ctx2, "push5")
	u.Is(0, pushed.GetSpanID(), "push5 span empty")
	u.Is(nil, req2, "Push5 req stays nil")
	u.Is(nil, ctx2, "Push5 ctx stays nil")
	u.Like(logs.Bytes(), "Push5 logs",
		"PushSpan[(][)]", "passed no Context")
	logs.Reset()

	spanReg.Halt()

	// Test errors

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
		"AddPairs":         func() { im.AddPairs("ok", true) },
		"AddAttribute":     func() {
			u.Is(nil, im.AddAttribute("ok", true), "empty AddAttrib err")
		},
	} {
		f()
		u.Like(logs.Bytes(), "empty "+m+" logs",
			"*disallowed method", "*import()ed span", `"_stack":`)
		logs.Reset()
	}

	ev := "TEST_ENV_INT"
	u.Is(11, EnvInteger(11, ev), "envint default")
	os.Setenv(ev, "101")
	u.Is(101, EnvInteger(11, ev), "envint from env")

	ex = u.GetPanic(func(){EnvInteger(2, "")})
	u.IsNot(nil, ex, "envint blank var exit")
	u.Like(logs.Bytes(), "envint blank var logs",
		"EnvInteger[(][)]", "*empty environment variable name", `"_file":`)
	logs.Reset()

	os.Setenv(ev, "45s")
	ex = u.GetPanic(func(){EnvInteger(11, ev)})
	u.IsNot(nil, ex, "envint invalid val exit")
	u.Like(logs.Bytes(), "envint invalid val logs",
		"*invalid integer value", `"EnvVar":"TEST_ENV_INT"`, `\\"45s\\"`)
	logs.Reset()

	ex = u.GetPanic(func(){
		req2, ctx2, span = RequestPushSpan(nil, context.Background(), "n/a")
	})
	u.Like(logs.Bytes(), "RequestPush nil req logs",
		"RequestPushSpan[(][)]", "passed nil Request", `"_stack":`)
	logs.Reset()

}
