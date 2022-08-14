// This module makes it easy to register a Span (and associated Trace)
// in GCP (Google Cloud Platform) CloudTrace (API v2).
//
// References to "ct2." refer to items imported from the
// "google.golang.org/api/cloudtrace/v2" module.  References to "lager."
// refer to items imported from "github.com/TyeMcQueen/go-lager".
// "spans." refers to "github.com/TyeMcQueen/go-lager/gcp-spans".
//
package trace

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/TyeMcQueen/go-lager"
	"github.com/TyeMcQueen/go-lager/gcp-spans"
	"github.com/TyeMcQueen/tools-gcp/conn"
	"github.com/TyeMcQueen/tools-gcp/metric"
	ct2 "google.golang.org/api/cloudtrace/v2"
//  api "google.golang.org/api/googleapi"
)

const ZuluTime = "2006-01-02T15:04:05.999999Z"

func TimeAsString(when time.Time) string {
	return when.In(time.UTC).Format(ZuluTime)
}

type Stringer interface {
	String() string
}

// See NewClient().
type Client struct {
	ss *ct2.ProjectsTracesSpansService
}

// Span tracks a span inside of a trace and can be used to create new child
// spans within it.  It also registers the span with GCP when Finish() is
// called on it [unless it was created via Import()].
//
// A Span object is expected to be modified only from a single goroutine
// and so no locking is implemented.  Creation of sub-spans does implement
// locking so that multiple go routines can safely create sub-spans from
// the same span without additional locking.
//
type Span struct {
	spans.ROSpan
	ch      chan<- Span
	start   time.Time
	end     time.Time
	parent  *Span
	details *ct2.Span

	mu      sync.Mutex // Lock used by NewSubSpan() for below items:
	spanInc uint64     // Amount to increment to make next span ID.
	kidSpan uint64     // The previous child span ID used.
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
	dones   <-chan bool
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
//      defer trace.StartServer(&ctx, 1)()
//      // Have 'ctx' returned by the http.Server.BaseContext func.
//
// This assumes that the calling function will not exit until the server
// is shutting down.
//
// You can also add an extra argument that is a pointer to a variable of
// type '*Registrar' to have that variable set to the span Registrar (mostly
// useful when testing).
//
func StartServer(
	pCtx *context.Context, runners int, pRegs ...**Registrar,
) func() {
	if nil == *pCtx {
		*pCtx = context.Background()
	}
	spanReg := MustNewRegistrar("", MustNewClient(*pCtx, nil), runners)
	*pCtx = spans.ContextStoreSpan(*pCtx, spanReg.NewFactory())
	for _, p := range pRegs {
		if nil != p {
			*p = spanReg
		}
	}
	return func() { spanReg.Halt() }
}

// NewRegistrar() starts a number of go-routines (given by 'runners') that
// wait to receive Finish()ed Spans and then register them with GCP Cloud
// Trace.
//
func NewRegistrar(
	project string, client Client, runners int,
) (*Registrar, error) {
	if "" == project {
		if dflt, err := lager.GcpProjectID(nil); nil != err {
			return nil, err
		} else {
			project = dflt
		}
	}
	queue, dones, err := startRegistrar(project, client, runners)
	if nil != err {
		return nil, err
	}
	return &Registrar{project, runners, queue, dones}, nil
}

// MustNewRegistrar() calls NewRegistrar() and, if that fails, uses
// lager.Exit() to abort the process.
//
func MustNewRegistrar(
	project string, client Client, runners int,
) *Registrar {
	reg, err := NewRegistrar(project, client, runners)
	if nil != err {
		lager.Exit().MMap("Could not start Registrar for CloudTrace spans",
			"err", err)
	}
	return reg
}

// WaitForIdleRunners() is only meant to be used by tests.  It allows you to
// ensure that all prior Finish()ed Spans have been processed so the test can
// check for any errors that were logged.
//
// It works by sending one request per runner that will cause that runner to
// wait when it receives it.  Then it reads the responses from all of the
// runners (which ends their waiting) and then returns.
//
func (r *Registrar) WaitForIdleRunners() {
	readys := make(chan Span, 0)
	empty := Span{ch: readys}
	for i := r.runners; 0 < i; i-- {
		r.queue <- empty
	}
	for i := r.runners; 0 < i; i-- {
		<-readys
	}
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
		_ = <-r.dones
	}
}

func EnvInteger(tacit int, envvar string) int {
	if "" == envvar {
		lager.Exit().WithCaller(1).List(
			"Empty environment variable name passed to EnvInteger()")
	}
	val := os.Getenv(envvar)
	if "" == val {
		return tacit
	}
	i, err := strconv.ParseInt(val, 10, 32)
	if nil != err {
		lager.Exit().MMap("Invalid integer value",
			"EnvVar", envvar, "Value", val, "Error", err)
	}
	return int(i)
}

func startRegistrar(
	project string, client Client, runners int,
) (chan<- Span, <-chan bool, error) {
	queue := make(chan Span, EnvInteger(1000, "SPAN_QUEUE_CAPACITY"))
	dones := make(chan bool, runners)
	prefix := "projects/" + project + "/"
	maxLag := conn.EnvDuration("SPAN_CREATE_TIMEOUT", "2s")
	capacity, err := metric.NewCapacityUsage(
		float64(cap(queue)), "span-queue", os.Getenv("LAGER_SPAN_PREFIX"), "1m")
	if nil != err {
		lager.Exit().MMap("Can't monitor span queue capacity", "error", err)
	}
	for ; 0 < runners; runners-- {
		go func() {
			for sp := range queue {
				capacity.Record(float64(len(queue)))
				// Sending an empty Span is used by tests to
				// wait for the previous CreateSpan() call(s) to finish:
				if 0 == sp.GetSpanID() {
					if sp.ch != queue {
						sp.ch <- sp
					}
					continue
				}
				ctx := context.Background()
				can := conn.Timeout(&ctx, maxLag)
				start := time.Now()
				_, err := client.ss.CreateSpan(
					prefix+sp.GetSpanPath(), sp.details,
				).Context(ctx).Do()
				if nil == err {
					spanCreated(start, "ok")
				} else if nil != ctx.Err() {
					spanCreated(start, "timeout")
				} else {
					spanCreated(start, "fail")
					lager.Fail().MMap("Failed to create span",
						"err", err, "span", sp.details)
				}
				can()
			}
			dones <- true
		}()
	}
	return queue, dones, nil
}

func (s *Span) initDetails() *Span {
	s.details = &ct2.Span{SpanId: spans.HexSpanID(s.GetSpanID())}
	if !s.start.IsZero() {
		s.details.StartTime = TimeAsString(s.start)
	}
	if nil != s.parent {
		s.details.ParentSpanId = spans.HexSpanID(s.parent.GetSpanID())
	}
	return s
}

// logIfEmpty() returns 'true' and logs an error with a stack trace if the
// invoking Factory is empty.  If 'orImported' is 'true', then this is also
// done if the Factory contains an Import()ed span.  Otherwise it logs
// nothing and returns 'false'.
//
func (s Span) logIfEmpty(orImported bool) bool {
	if 0 == s.GetSpanID() {
		lager.Fail().WithStack(1, -1).MMap(
			"Disallowed method called on empty spans.Factory")
		return true
	} else if !s.end.IsZero() {
		lager.Fail().WithStack(1, -1).MMap(
			"Disallowed method called on Finish()ed spans.Factory",
			"spanName", s.details.DisplayName)
		return true
	} else if orImported && s.start.IsZero() {
		lager.Fail().WithStack(1, -1).MMap(
			"Disallowed method called on Import()ed spans.Factory")
		return true
	}
	return false
}

// GetStart() returns the time at which the span began.  Returns a zero
// time if the Factory is empty or the contained span was Import()ed.
//
func (s Span) GetStart() time.Time {
	return s.start
}

// GetDuration() returns a negative duration if the Factory is empty or
// if the span has not been Finish()ed yet.  Otherwise, it returns the
// span's duration.
//
func (s Span) GetDuration() time.Duration {
	if s.end.IsZero() {
		return -time.Second
	}
	return s.end.Sub(s.start)
}

// Import() returns a new Factory containing a span created somewhere
// else.  If the traceID or spanID is invalid, then a 'nil' Factory and
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

// ImportFromHeaders() returns a new Factory containing a span created
// somewhere else based on the "X-Cloud-Trace-Context:" header.  If the
// header does not contain a valid CloudContext value, then a valid but
// empty Factory is returned.
//
func (s Span) ImportFromHeaders(headers http.Header) spans.Factory {
	roSpan := s.ROSpan.ImportFromHeaders(headers)
	sp := &Span{ROSpan: roSpan.(spans.ROSpan), ch: s.ch}
	return sp
}

// NewTrace() returns a new Factory holding a new span, part of a new
// trace.  Any span held in the invoking Factory is ignored.
//
func (s Span) NewTrace() spans.Factory {
	ROSpan, err := s.ROSpan.Import(
		NewTraceID(s.GetTraceID()), NewSpanID(s.GetSpanID()))
	sp := &Span{ROSpan: ROSpan.(spans.ROSpan), ch: s.ch}
	if nil != err {
		lager.Fail().MMap("Impossibly got invalid trace/span ID", "err", err)
		return sp
	}
	sp.start = time.Now()
	return sp.initDetails()
}

// NewSubSpan() returns a new Factory holding a new span that is a
// sub-span of the span contained in the invoking Factory.  If the
// invoking Factory was empty, then a failure with a stack trace is
// logged and the empty Factory is returned.
//
// NewSubSpan() locks the calling span so that you can safely call
// NewSubSpan() on the same parent span from multiple go routines.
//
func (s *Span) NewSubSpan() spans.Factory {
	if s.logIfEmpty(false) {
		return s
	}

	s.mu.Lock()
	if 0 == s.kidSpan { // Creating first sub-span
		s.kidSpan = s.GetSpanID()    // Want kidSpan to be spanID+spanInc below
		s.spanInc = 1 | NewSpanID(0) // Must be odd; mutually prime to 2**64
	}
	s.kidSpan += s.spanInc
	if 0 == s.kidSpan { // Eventually we can rotate to 0...
		s.kidSpan += s.spanInc // ...so rotate one more time.
	}
	if nil != s.details {
		s.details.ChildSpanCount++
	}
	ro := s.ROSpan
	ro.SetSpanID(s.kidSpan)
	s.mu.Unlock()

	kid := &Span{ROSpan: ro, ch: s.ch, start: time.Now()}
	kid.parent = s
	kid.initDetails()
	if !s.start.IsZero() {
		kid.details.SameProcessAsParentSpan = true
	}
	return kid
}

// NewSpan() returns a new Factory holding a new span; either NewTrace() or
// NewSubSpan(), depending on whether the invoking Factory is empty.
//
func (s *Span) NewSpan() spans.Factory {
	if 0 == s.GetSpanID() {
		return s.NewTrace()
	}
	return s.NewSubSpan()
}

// Sets the span kind to "SERVER".  Does nothing except log a failure
// with a stack trace if the Factory is empty or Import()ed.  Always returns
// the calling Factory so further method calls can be chained.
//
func (s *Span) SetIsServer() spans.Factory {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "SERVER"
	}
	return s
}

// Sets the span kind to "CLIENT".  Does nothing except log a failure
// with a stack trace if the Factory is empty or Import()ed.  Always returns
// the calling Factory so further method calls can be chained.
//
func (s *Span) SetIsClient() spans.Factory {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "CLIENT"
	}
	return s
}

// Sets the span kind to "PRODUCER".  Does nothing except log a failure
// with a stack trace if the Factory is empty or Import()ed.  Always returns
// the calling Factory so further method calls can be chained.
//
func (s *Span) SetIsPublisher() spans.Factory {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "PRODUCER"
	}
	return s
}

// Sets the span kind to "CONSUMER".  Does nothing except log a failure
// with a stack trace if the Factory is empty or Import()ed.  Always returns
// the calling Factory so further method calls can be chained.
//
func (s *Span) SetIsSubscriber() spans.Factory {
	if !s.logIfEmpty(true) {
		s.details.SpanKind = "CONSUMER"
	}
	return s
}

// SetDisplayName() sets the display name on the contained span.  Does
// nothing except log a failure with a stack trace if the Factory is
// empty or Import()ed.  Always returns the calling Factory so further
// method calls can be chained.
//
func (s *Span) SetDisplayName(desc string) spans.Factory {
	if !s.logIfEmpty(true) {
		if "" == desc {
			s.details.DisplayName = nil
		} else {
			if nil == s.details.DisplayName {
				s.details.DisplayName = &ct2.TruncatableString{}
			}
			s.details.DisplayName.Value = desc
		}
	}
	return s
}

// AddAttribute() adds an attribute key/value pair to the contained span.
// Does nothing except log a failure with a stack trace if the Factory is
// empty or Import()ed (even returning a 'nil' error).
//
// 'val' can be a 'string', 'int64', or a 'bool'.  'int' values will be
// promoted to 'int64'.  Other values that have a String() or an Error()
// method will have that method used to convert them to a string.  If
// 'key' is empty or 'val' is not one of the listed types, then an error
// is returned and the attribute is not added.
//
func (s *Span) AddAttribute(key string, val interface{}) error {
	if s.logIfEmpty(true) {
		return nil
	}
	return s.addAttribute(key, val, false)
}

// addAttribute() is AddAttribute() but can be told to silently ignore zero
// values ('0', 'false', 'nil') for use by AddPairs().
//
func (s *Span) addAttribute(key string, val interface{}, noZero bool) error {
	if "" == key {
		return fmt.Errorf("AddAttribute(): 'key' must not be empty string")
	}
	var av ct2.AttributeValue
	if noZero && nil == val {
		return nil
	}
	switch t := val.(type) {
	case string:
		av.StringValue = &ct2.TruncatableString{Value: t}
	case int64:
		if noZero && 0 == t {
			return nil
		}
		av.IntValue = t
	case int:
		if noZero && 0 == t {
			return nil
		}
		av.IntValue = int64(t)
	case bool:
		if noZero && !t {
			return nil
		}
		av.BoolValue = t
	case error:
		av.StringValue = &ct2.TruncatableString{Value: t.Error()}
	case Stringer:
		av.StringValue = &ct2.TruncatableString{Value: t.String()}
	default:
		return fmt.Errorf("AddAttribute(): Invalid value type (%T)", val)
	}
	if nil == s.details.Attributes {
		s.details.Attributes = &ct2.Attributes{
			AttributeMap: make(map[string]ct2.AttributeValue),
		}
	}
	s.details.Attributes.AttributeMap[key] = av
	return nil
}

// AddPairs() takes a list of attribute key/value pairs.  For each pair,
// AddAttribute() is called and any returned error is logged (including
// a reference to the line of code that called AddPairs).  Always returns
// the calling Factory so further method calls can be chained.
//
// AddPairs() silently ignores 'zero' values except "" ('0', 'false', 'nil')
// rather than either logging an error or adding them only to have the value
// show up as "undefined".
//
// Does nothing except log a single failure with a stack trace if the
// Factory is empty or Import()ed.
//
func (s *Span) AddPairs(pairs ...interface{}) spans.Factory {
	if s.logIfEmpty(true) {
		return s
	}
	log := lager.Fail().WithCaller(1)
	for i := 0; i < len(pairs); i += 2 {
		ix := pairs[i]
		if len(pairs) <= i+1 {
			log.MMap("Ignoring unpaired last arg to trace.Span AddPairs()",
				"arg", ix)
		} else if key, ok := ix.(string); !ok {
			log.MMap("Non-string key passed to trace.Span AddPairs()",
				"type", fmt.Sprintf("%T", ix), "key", ix, "arg index", i)
		} else if err := s.addAttribute(key, pairs[i+1], true); nil != err {
			log.MMap("Error adding attribute to Span",
				"key", key, "val", pairs[i+1], "error", err)
		}
	}
	return s
}

// SetStatusCode() sets the status code on the contained span.
// 'code' is expected to be a value from
// google.golang.org/genproto/googleapis/rpc/code but this is not
// verified.  HTTP status codes are also understood by the library.
// Does nothing except log a failure with a stack trace if the Factory
// is empty or Import()ed.  Always returns the calling Factory so further
// method calls can be chained.
//
func (s *Span) SetStatusCode(code int64) spans.Factory {
	if s.logIfEmpty(true) {
		return s
	}
	if nil == s.details.Status {
		s.details.Status = &ct2.Status{}
	}
	s.details.Status.Code = code
	return s
}

// SetStatusMessage() sets the status message string on the contained
// span.  By convention, only a failure should set a status message.
// Does nothing except log a failure with a stack trace if the Factory
// is empty or Import()ed.  Always returns the calling Factory so further
// method calls can be chained.
//
func (s *Span) SetStatusMessage(msg string) spans.Factory {
	if s.logIfEmpty(true) {
		return s
	}
	if nil == s.details.Status {
		s.details.Status = &ct2.Status{}
	}
	s.details.Status.Message = msg
	return s
}

// Finish() notifies the Factory that the contained span is finished.
// The Factory will be read-only (as if empty) afterward.  The Factory will
// arrange for the span to be registered.
//
// The returned value is the duration of the span's life.  If the Factory
// was already empty or the contained span was Import()ed, then a failure
// with a stack trace is logged and a 0 duration is returned.
//
func (s *Span) Finish() time.Duration {
	if s.logIfEmpty(true) {
		return time.Duration(0)
	}
	if nil == s.details.DisplayName {
		s.SetDisplayName(os.Args[0])
	}
	s.end = time.Now()
	s.details.EndTime = TimeAsString(s.end)
	select {
	case s.ch <- *s:
	default:
		spanDropped()
	}
	return s.end.Sub(s.start)
}
