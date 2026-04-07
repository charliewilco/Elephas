package elephas

import (
	"context"
	"log/slog"
	"time"
)

type Service struct {
	store            Store
	extractor        Extractor
	cache            ContextCache
	logger           *slog.Logger
	resolveThreshold float64
	now              func() time.Time
}

type ServiceOption func(*Service)

func NewService(store Store, opts ...ServiceOption) *Service {
	svc := &Service{
		store:            store,
		logger:           slog.Default(),
		resolveThreshold: 0.85,
		now:              func() time.Time { return time.Now().UTC() },
	}

	for _, opt := range opts {
		opt(svc)
	}

	return svc
}

func WithExtractor(extractor Extractor) ServiceOption {
	return func(s *Service) {
		s.extractor = extractor
	}
}

func WithContextCache(cache ContextCache) ServiceOption {
	return func(s *Service) {
		s.cache = cache
	}
}

func WithLogger(logger *slog.Logger) ServiceOption {
	return func(s *Service) {
		if logger != nil {
			s.logger = logger
		}
	}
}

func WithResolveThreshold(threshold float64) ServiceOption {
	return func(s *Service) {
		if threshold > 0 && threshold <= 1 {
			s.resolveThreshold = threshold
		}
	}
}

func WithClock(now func() time.Time) ServiceOption {
	return func(s *Service) {
		if now != nil {
			s.now = now
		}
	}
}

func (s *Service) Ping(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *Service) Close() error {
	if s.cache != nil {
		if err := s.cache.Close(); err != nil {
			return err
		}
	}

	return s.store.Close()
}

func (s *Service) loggerFor(ctx context.Context, component string) *slog.Logger {
	attrs := []any{"component", component}
	if requestID := RequestIDFromContext(ctx); requestID != "" {
		attrs = append(attrs, "request_id", requestID)
	}
	return s.logger.With(attrs...)
}
