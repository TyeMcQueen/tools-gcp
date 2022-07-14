// This module makes it easy to register a Span (and associated Trace)
// in GCP (Google Cloud Platform) CloudTrace (API v2).
//
// References to "ct2." refer to items imported from the
// "google.golang.org/api/cloudtrace/v2" module.  References to "lager."
// refer to items imported from "github.com/TyeMcQueen/go-lager".
//
package trace

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-lager/gcp-spans"
	ct2 "google.golang.org/api/cloudtrace/v2"
//  api "google.golang.org/api/googleapi"
)

const ZuluTime = "2006-01-02T15:04:05.999999Z"

func TimeAsString(when time.Time) string {
	return when.In(time.UTC).Format(ZuluTime)
}

// See NewClient().
type Client struct {
	ss *ct2.ProjectsTracesSpansService
}

// Span tracks a span inside of a trace and can be used to create new child
// spans within it.  It also registers the span with GCP when Finish() is
// called on it [unless it was created via Import()].
//
// A Span object is expected to be used only from a single goroutine and so
// no locking is implemented.  If you wish to use a single Span object from
// multiple goroutines, then you'll need to use your own sync.Mutex or
// similar.
//
type Span struct {
	spans.ROSpan
	ch      chan<- Span
	spanInc uint64 // Amount to increment to make next span ID.
	kidSpan uint64 // The previous child span ID used.
	momSpan uint64
	start   time.Time
	end     time.Time
	details *ct2.Span
}

// Registrar is mostly just an object to use to Halt() the registration
// runners that got started when you created the Registrar.
//
// It also can create an empty spans.Factory that can be used to create and
// manipulate spans.
//
type Registrar struct {
	proj    string
	runners int
	queue   chan<- Span
	done    <-chan bool
}

var warnOnce sync.Once

// NewSpanID() just generates a random uint64 value.  You are never expected
// to call this directly.  It prefers to use cryptographically strong random
// values but will resort to math/rand.Uint64() if that fails.  Such a
// failure will be logged via lager.Warn() only once but the attempt is
// always made.
//
func NewSpanID(oldSpanID uint64) (spanID uint64) {
	err := binary.Read(crand.Reader, binary.LittleEndian, &spanID)
	if nil != err {
		warnOnce.Do(func() {
			lager.Warn().MMap(
				"Error reading random bytes for new trace/span ID",
				"error", err)
		})
	}
	for 0 == spanID {
		spanID = oldSpanID + mrand.Uint64()
	}
	return
}

// NewTraceID() returns a new trace ID that can be used with GCP CloudTrace.
// It is just a randomly generated sequence of 32 hexadecimal digits.  You
// are not expected to call this directly.
//
// If 'oldTraceID' is a valid trace ID, then it is used to add more
// randomness to the new trace ID (and can't return that same trace ID).
//
func NewTraceID(oldTraceID string) string {
	one := NewSpanID(0)
	two := NewSpanID(0)
	if 32 == len(oldTraceID) {
		add, _ := strconv.ParseUint(oldTraceID[0:16], 16, 64)
		one += add
		add, _ = strconv.ParseUint(oldTraceID[16:32], 16, 64)
		two += add
		if 0 == one && 0 == two {
			two -= add
		}
	}
	return spans.HexSpanID(one) + spans.HexSpanID(two)
}

// NewClient() creates a new client capable of registering Spans in the GCP
// CloudTrace API v2.  This client has no methods but should be passed in
// when starting the Registrar.
//
// To get a default connection, pass in 'nil' for 'svc'.  Otherwise you can
// use ct2.NewService() or ct2.New() to create a base service to use and
// pass the result in as 'svc'.
//
// If 'svc' is 'nil', then 'ctx' is the Context used when creating the base
// service using default options.  If 'svc' is not 'nil', then 'ctx' is
// ignored.
//
func NewClient(ctx context.Context, svc *ct2.Service) (Client, error) {
	if nil == svc {
		if nil == ctx {
			ctx = context.Background()
		}
		if newSvc, err := ct2.NewService(ctx); nil != err {
			return Client{}, err
		} else {
			svc = newSvc
		}
	}
	return Client{ss: ct2.NewProjectsTracesSpansService(svc)}, nil
}

// MustNewClient() calls NewClient().  If that fails, then lager.Exit() is
// used to log the error and abort the process.
//
func MustNewClient(ctx context.Context, svc *ct2.Service) Client {
	client, err := NewClient(ctx, svc)
	if nil != err {
		lager.Exit(ctx).MMap("Failed to create CloudTrace client",
			"err", err)
	}
	return client
}

// StartServer() is the simplest start-up code to include in a server to
// enable GCP-based tracing, usually called like:
//
//      ctx := context.Background()
//      defer trace.StartServer(&ctx)()
//      // Have 'ctx' returned by the http.Server.BaseContext func.
//
// This assumes that the calling function will not exit until the server
// is shutting down.
//
func StartServer(pCtx *context.Context, runners int) func() {
	spanReg := MustNewRegistrar("", MustNewClient(*pCtx, nil), runners)
	*pCtx = spans.ContextStoreSpan(*pCtx, spanReg.NewFactory())
	return func() { spanReg.Halt() }
}

// NewRegistrar() starts a number of go-routines (given by 'runners') that
// wait to receive Finish()ed Spans and then register them with GCP Cloud
// Trace.
//
func NewRegistrar(
	project string, client Client, runners int,
) (Registrar, error) {
	queue, done, err := startRegistrar(project, client, runners)
	if nil != err {
		return Registrar{}, err
	}
	return Registrar{project, runners, queue, done}, nil
}

// MustNewRegistrar() calls NewRegistrar() and, if that fails, uses
// lager.Exit() to abort the process.
//
func MustNewRegistrar(
	project string, client Client, runners int,
) Registrar {
	reg, err := NewRegistrar(project, client, runners)
	if nil != err {
		lager.Exit().MMap("Could not start Registrar for CloudTrace spans",
			"err", err)
	}
	return reg
}

// NewFactory() returns a spans.Factory that can be used to create and
// manipulate spans and eventually register them with GCP Cloud Trace.
//
func (r *Registrar) NewFactory() spans.Factory {
	return &Span{ROSpan: spans.NewROSpan(r.proj), ch: r.queue}
}

// Halt() tells the runners to terminate and waits for them all to finish
// before returning.
//
// Halt() should only be called after you are sure that no more spans will
// be Finish()ed.  Any spans Finish()ed after Halt() has been called may
// cause a panic().  Not waiting for Halt() to return can mean that recently
// Finish()ed spans might not be registered.
//
func (r *Registrar) Halt() {
	if nil == r.queue {
		return
	}
	close(r.queue)
	r.queue = nil
	for ; 0 < r.runners; r.runners-- {
		_ = <-r.done
	}
}

func startRegistrar(
	project string, client Client, runners int,
) (chan<- Span, <-chan bool, error) {
	queue := make(chan Span, runners)
	done := make(chan bool, runners)
	if "" == project {
		if dflt, err := lager.GcpProjectID(nil); nil != err {
			return nil, nil, err
		} else {
			project = dflt
		}
	}
	prefix := "projects/" + project + "/"
	for ; 0 < runners; runners-- {
		go func() {
			for sp := range queue {
				// TODO!  Add a reasonable timeout!!
				_, err := client.ss.CreateSpan(
					prefix+sp.GetSpanPath(), sp.details,
				).Do()
				if nil != err {
					lager.Fail().MMap("Failed to create span",
						"err", err, "span", sp.details)
				}
			}
			done <- true
		}()
	}
	return queue, done, nil
}

func (s *Span) initDetails() *Span {
	s.details = &ct2.Span{SpanId: spans.HexSpanID(s.GetSpanID())}
	if !s.start.IsZero() {
		s.details.StartTime = TimeAsString(s.start)
	}
	if 0 != s.momSpan {
		s.details.ParentSpanId = spans.HexSpanID(s.momSpan)
	}
	return s
}

// logIfEmpty() returns 'true' and logs an error with a stack trace if the
// invoking factory is empty.  If 'orImported' is 'true', then this is also
// done if the factory contains an Import()ed span.  Otherwise it logs
// nothing and returns 'false'.
//
func (s Span) logIfEmpty(orImported bool) bool {
	if 0 == s.GetSpanID() {
		lager.Fail().WithStack(1, -1, 3).List(
			"Disallowed method called on empty spans.Factory")
		return true
	} else if orImported && s.start.IsZero() {
		lager.Fail().WithStack(1, -1, 3).List(
			"Disallowed method called on Import()ed spans.Factory")
		return true
	}
	return false
}

// GetStart() returns the time at which the span began.  Returns a zero
// time if the factory is empty or the contained span was Import()ed.
//
func (s Span) GetStart() time.Time {
	return s.start
}

// Import() returns a new factory containing a span created somewhere
// else.  If the traceID or spanID is invalid, then a 'nil' factory and
// an error are returned.  The usual reason to do this is so that you can
// then call NewSubSpan().
//
func (s Span) Import(traceID string, spanID uint64) (spans.Factory, error) {
	ROSpan, err := s.ROSpan.Import(traceID, spanID)
	if nil != err {
		return nil, err
	}
	sp := &Span{ROSpan: ROSpan.(spans.ROSpan), ch: s.ch}
	return sp, nil
}

func (s Span) ImportFromHeaders(headers http.Header) spans.Factory {
	ROSpan := s.ROSpan.ImportFromHeaders(headers)
	sp := &Span{ROSpan: ROSpan.(spans.ROSpan), ch: s.ch}
	return sp
}

// NewTrace() returns a new factory holding a new span, part of a new
// trace.  Any span held in the invoking factory is ignored.
//
func (s Span) NewTrace() spans.Factory {
	ROSpan, err := s.ROSpan.Import(
		NewTraceID(s.GetTraceID()), NewSpanID(s.GetSpanID()))
	if nil != err {
		lager.Fail().MMap("Impossibly got invalid trace/span ID", "err", err)
		return nil
	}
	sp := &Span{ROSpan: ROSpan.(spans.ROSpan), ch: s.ch, start: time.Now()}
	return sp.initDetails()
}

// NewSubSpan() returns a new factory holding a new span that is a
// sub-span of the span contained in the invoking factory.  If the
// invoking factory was empty, then a failure with a stack trace is
// logged and a 'nil' factory is returned.
//
func (s Span) NewSubSpan() spans.Factory {
	if s.logIfEmpty(false) {
		return nil
	}
	if 0 == s.kidSpan { // Creating first sub-span
		s.kidSpan = s.GetSpanID() // Want kidSpan to be spanID+spanInc below
		s.spanInc = 1 | NewSpanID(0) // Must be odd; mutually prime to 2**64
	}
	s.kidSpan += s.spanInc
	if 0 == s.kidSpan { // Eventually we can rotate to 0...
		s.kidSpan += s.spanInc // ...so rotate one more time.
	}
	if nil != s.details {
		s.details.ChildSpanCount++
	}

	ROSpan := s.ROSpan
	ROSpan.SetSpanID(s.kidSpan)
	kid := &Span{ROSpan: ROSpan, ch: s.ch, start: time.Now()}
	kid.initDetails()
	if !s.start.IsZero() {
		kid.details.SameProcessAsParentSpan = true
	}
	return kid
}

// NewSpan() returns a new factory holding a new span; either NewTrace() or
// NewSubSpan(), depending on whether the invoking factory is empty.
//
func (s Span) NewSpan() spans.Factory {
	if 0 == s.GetSpanID() {
		return s.NewTrace()
	}
	return s.NewSubSpan()
}

// Sets the span kind to "SERVER".  Does nothing except log a failure
// with a stack trace if the factory is empty or Import()ed.
//
func (s *Span) SetIsServer() {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "SERVER"
	}
}

// Sets the span kind to "CLIENT".  Does nothing except log a failure
// with a stack trace if the factory is empty or Import()ed.
//
func (s *Span) SetIsClient() {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "CLIENT"
	}
}

// Sets the span kind to "PRODUCER".  Does nothing except log a failure
// with a stack trace if the factory is empty or Import()ed.
//
func (s *Span) SetIsPublisher() {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "PRODUCER"
	}
}

// Sets the span kind to "CONSUMER".  Does nothing except log a failure
// with a stack trace if the factory is empty or Import()ed.
//
func (s *Span) SetIsSubscriber() {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "CONSUMER"
	}
}

// SetDisplayName() sets the display name on the contained span.  Does
// nothing except log a failure with a stack trace if the factory is
// empty or Import()ed.
//
func (s *Span) SetDisplayName(desc string) {
	if !s.logIfEmpty(true) {
		if nil == s.details.DisplayName {
			s.details.DisplayName = &ct2.TruncatableString{}
		}
		s.details.DisplayName.Value = desc
	}
}

// AddAttribute() adds an attribute key/value pair to the contained span.
// Does nothing except log a failure with a stack trace if the factory is
// empty or Import()ed (even returning a 'nil' error).
//
// 'val' can be a 'string', an 'int' or 'int64', or a 'bool'.  If 'key'
// is empty or 'val' is not one of the listed types, then an error is
// returned and the attribute is not added.
//
func (s *Span) AddAttribute(key string, val interface{}) error {
	if s.logIfEmpty(true) {
		return nil
	}
	if nil == s.details.Attributes {
		s.details.Attributes = &ct2.Attributes{
			AttributeMap: make(map[string]ct2.AttributeValue),
		}
	}
	var av ct2.AttributeValue
	switch t := val.(type) {
	case string:
		av.StringValue = &ct2.TruncatableString{Value: t}
	case int64:
		av.IntValue = t
	case int:
		av.IntValue = int64(t)
	case bool:
		av.BoolValue = t
	default:
		return fmt.Errorf("AddAttribute(): Invalid value type (%T)", val)
	}
	s.details.Attributes.AttributeMap[key] = av
	return nil
}

// SetStatusCode() sets the status code on the contained span.
// 'code' is expected to be a value from
// google.golang.org/genproto/googleapis/rpc/code but this is not
// verified.  Does nothing except log a failure with a stack trace
// if the factory is empty or Import()ed.
//
func (s *Span) SetStatusCode(code int64) {
	if s.logIfEmpty(true) {
		return
	}
	if nil == s.details.Status {
		s.details.Status = &ct2.Status{}
	}
	s.details.Status.Code = code
}

// SetStatusMessage() sets the status message string on the contained
// span.  Does nothing except log a failure with a stack trace if the
// factory is empty or Import()ed.
//
func (s *Span) SetStatusMessage(msg string) {
	if s.logIfEmpty(true) {
		return
	}
	if nil == s.details.Status {
		s.details.Status = &ct2.Status{}
	}
	s.details.Status.Message = msg
}

// Finish() notifies the factory that the contained span is finished.
// The factory will be empty afterward.  The factory will arrange for the
// span to be registered.
//
// The returned value is the duration of the span's life.  If the factory
// was already empty or the contained span was from Import(), then a
// failure with a stack trace is logged and a 0 duration is returned.
//
func (s *Span) Finish() time.Duration {
	if s.logIfEmpty(true) {
		return time.Duration(0)
	}
	s.end = time.Now()
	s.details.EndTime = TimeAsString(s.end)
	s.ch <- *s
	return s.end.Sub(s.start)
}
