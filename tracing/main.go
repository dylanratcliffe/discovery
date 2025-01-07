package tracing

import (
	"context"
	"fmt"
	"os"
	"runtime/debug"
	"slices"
	"time"

	_ "embed"

	"github.com/MrAlias/otel-schema-utils/schema"
	"github.com/getsentry/sentry-go"
	log "github.com/sirupsen/logrus"
	"github.com/spf13/viper"
	"go.opentelemetry.io/contrib/detectors/aws/ec2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// ServiceVersion is the version of the service. This will be overridden by the
// build system, using:
// go build -ldflags "-X github.com/overmindtech/api-server/tracing.ServiceVersion=$(git describe --tags --exact-match 2>/dev/null || git rev-parse --short HEAD)" -o your-app
//
// This allows our change detection workflow to work correctly. If we were
// embedding the version here each time we would always produce a slightly
// different compiled binary, and therefore it would look like there was a
// change each time
var ServiceVersion = "dev"

const instrumentationName = "github.com/overmindtech/discovery"

var (
	tracer = otel.GetTracerProvider().Tracer(
		instrumentationName,
		trace.WithInstrumentationVersion(ServiceVersion),
		trace.WithSchemaURL(semconv.SchemaURL),
	)
)

func Tracer() trace.Tracer {
	return tracer
}

func tracingResource() *resource.Resource {
	// Identify your application using resource detection
	resources := []*resource.Resource{}

	// the EC2 detector takes ~10s to time out outside EC2
	// disable it if we're running from a git checkout
	_, err := os.Stat(".git")
	if os.IsNotExist(err) {
		ec2Res, err := resource.New(context.Background(), resource.WithDetectors(ec2.NewResourceDetector()))
		if err != nil {
			log.WithError(err).Error("error initialising EC2 resource detector")
			return nil
		}
		resources = append(resources, ec2Res)
	}

	// Needs https://github.com/open-telemetry/opentelemetry-go-contrib/issues/1856 fixed first
	// // the EKS detector is temperamental and doesn't like running outside of kube
	// // hence we need to keep it from running when we know there's no kube
	// if !viper.GetBool("disable-kube") {
	// 	// Use the AWS resource detector to detect information about the runtime environment
	// 	detectors = append(detectors, eks.NewResourceDetector())
	// }

	hostRes, err := resource.New(context.Background(),
		resource.WithHost(),
		resource.WithOS(),
		resource.WithProcess(),
		resource.WithContainer(),
		resource.WithTelemetrySDK(),
	)
	if err != nil {
		log.WithError(err).Error("error initialising host resource")
		return nil
	}
	resources = append(resources, hostRes)

	localRes, err := resource.New(context.Background(),
		resource.WithSchemaURL(semconv.SchemaURL),
		// Add your own custom attributes to identify your application
		resource.WithAttributes(
			semconv.ServiceNameKey.String("discovery"),
			semconv.ServiceVersionKey.String(ServiceVersion),
		),
	)
	if err != nil {
		log.WithError(err).Error("error initialising local resource")
		return nil
	}
	resources = append(resources, localRes)

	conv := schema.NewConverter(schema.DefaultClient)
	res, err := conv.MergeResources(context.Background(), semconv.SchemaURL, resources...)

	if err != nil {
		log.WithError(err).Error("error merging resource")
		return nil
	}
	return res
}

var tp *sdktrace.TracerProvider

func InitTracerWithHoneycomb(key string, opts ...otlptracehttp.Option) error {
	if key != "" {
		opts = append(opts,
			otlptracehttp.WithEndpoint("api.honeycomb.io"),
			otlptracehttp.WithHeaders(map[string]string{"x-honeycomb-team": key}),
		)
	}
	return InitTracer(opts...)
}

func InitTracer(opts ...otlptracehttp.Option) error {
	if sentry_dsn := viper.GetString("sentry-dsn"); sentry_dsn != "" {
		var environment string
		if viper.GetString("run-mode") == "release" {
			environment = "prod"
		} else {
			environment = "dev"
		}
		err := sentry.Init(sentry.ClientOptions{
			Dsn:              sentry_dsn,
			AttachStacktrace: true,
			EnableTracing:    false,
			Environment:      environment,
			// Set TracesSampleRate to 1.0 to capture 100%
			// of transactions for performance monitoring.
			// We recommend adjusting this value in production,
			TracesSampleRate: 1.0,
		})
		if err != nil {
			log.Errorf("sentry.Init: %s", err)
		}
		// setup recovery for an unexpected panic in this function
		defer sentry.Flush(2 * time.Second)
		defer sentry.Recover()
		log.Info("sentry configured")
	}

	client := otlptracehttp.NewClient(opts...)
	otlpExp, err := otlptrace.New(context.Background(), client)
	if err != nil {
		return fmt.Errorf("creating OTLP trace exporter: %w", err)
	}
	log.Infof("otlptracehttp client configured itself: %v", client)

	tracerOpts := []sdktrace.TracerProviderOption{
		sdktrace.WithBatcher(otlpExp),
		sdktrace.WithResource(tracingResource()),
		sdktrace.WithSampler(sdktrace.ParentBased(NewUserAgentSampler(200, "ELB-HealthChecker/2.0", "kube-probe/1.27+"))),
	}
	if viper.GetBool("stdout-trace-dump") {
		stdoutExp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return err
		}
		tracerOpts = append(tracerOpts, sdktrace.WithBatcher(stdoutExp))
	}
	tp = sdktrace.NewTracerProvider(tracerOpts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return nil
}

// nolint: contextcheck // deliberate use of local context to avoid getting tangled up in any existing timeouts or cancels
func ShutdownTracer() {
	// Flush buffered events before the program terminates.
	defer sentry.Flush(5 * time.Second)

	// ensure that we do not wait indefinitely on the trace provider shutdown
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := tp.ForceFlush(ctx); err != nil {
		log.WithContext(ctx).Printf("Error flushing tracer provider: %v", err)
	}
	if err := tp.Shutdown(ctx); err != nil {
		log.WithContext(ctx).Printf("Error shutting down tracer provider: %v", err)
	}
}

type UserAgentSampler struct {
	userAgents          []string
	innerSampler        sdktrace.Sampler
	sampleRateAttribute attribute.KeyValue
}

func NewUserAgentSampler(sampleRate int, userAgents ...string) *UserAgentSampler {
	var innerSampler sdktrace.Sampler
	switch {
	case sampleRate <= 0:
		innerSampler = sdktrace.NeverSample()
	case sampleRate == 1:
		innerSampler = sdktrace.AlwaysSample()
	default:
		innerSampler = sdktrace.TraceIDRatioBased(1.0 / float64(sampleRate))
	}
	return &UserAgentSampler{
		userAgents:          userAgents,
		innerSampler:        innerSampler,
		sampleRateAttribute: attribute.Int("SampleRate", sampleRate),
	}
}

// ShouldSample returns a SamplingResult based on a decision made from the
// passed parameters.
func (h *UserAgentSampler) ShouldSample(parameters sdktrace.SamplingParameters) sdktrace.SamplingResult {
	for _, attr := range parameters.Attributes {
		if (attr.Key == "http.user_agent" || attr.Key == "user_agent.original") && slices.Contains(h.userAgents, attr.Value.AsString()) {
			result := h.innerSampler.ShouldSample(parameters)
			if result.Decision == sdktrace.RecordAndSample {
				result.Attributes = append(result.Attributes, h.sampleRateAttribute)
			}
			return result
		}
	}

	return sdktrace.AlwaysSample().ShouldSample(parameters)
}

// Description returns information describing the Sampler.
func (h *UserAgentSampler) Description() string {
	return "Simple Sampler based on the UserAgent of the request"
}

// LogRecoverToReturn Recovers from a panic, logs and forwards it sentry and otel, then returns
// Does nothing when there is no panic.
func LogRecoverToReturn(ctx context.Context, loc string) {
	err := recover()
	if err == nil {
		return
	}

	stack := string(debug.Stack())
	HandleError(ctx, loc, err, stack)
}

// LogRecoverToExit Recovers from a panic, logs and forwards it sentry and otel, then exits
// Does nothing when there is no panic.
func LogRecoverToExit(ctx context.Context, loc string) {
	err := recover()
	if err == nil {
		return
	}

	stack := string(debug.Stack())
	HandleError(ctx, loc, err, stack)

	// ensure that errors still get sent out
	ShutdownTracer()

	os.Exit(1)
}

func HandleError(ctx context.Context, loc string, err interface{}, stack string) {
	msg := fmt.Sprintf("unhandled panic in %v, exiting: %v", loc, err)

	hub := sentry.CurrentHub()
	if hub != nil {
		hub.Recover(err)
	}

	if ctx != nil {
		log.WithContext(ctx).WithFields(log.Fields{"loc": loc, "stack": stack}).Error(msg)
		span := trace.SpanFromContext(ctx)
		span.SetAttributes(attribute.String("ovm.panic.loc", loc))
		span.SetAttributes(attribute.String("ovm.panic.stack", stack))
	} else {
		log.WithFields(log.Fields{"loc": loc, "stack": stack}).Error(msg)
	}
}
