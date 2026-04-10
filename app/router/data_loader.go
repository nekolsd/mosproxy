package router

import (
	"crypto/sha256"
	"os"
	"sync/atomic"

	"github.com/rs/zerolog"
)

type Dataloader interface {
	// Load T and stage the T. Return false if error ocurred.
	LoadAndStage() (ok bool)

	// Commit the change. If no T staged, this call is noop.
	Commit()

	// Discard the change. If no T staged, this call is noop.
	Discard()
}

// Provider data.
// Do not retain the result of V(), it may change after router reloaded.
type DataProvider[V any] interface {
	V() *V
}

// FileLoader loads and hot-reloads a single file.
// Funcs of FileLoader are not concurrent safe, except V().
type FileLoader[V any] struct {
	fp      string
	parseFn func(b []byte) (*V, error)
	logger  *zerolog.Logger
	vInfo   func(e *zerolog.Event, v *V)

	hash       [sha256.Size]byte
	v          atomic.Pointer[V]
	stagedHash [sha256.Size]byte
	staged     *V
}

func NewFileLoader[V any](
	fp string,
	parseFn func(b []byte) (*V, error),
	logger *zerolog.Logger,
	vInfo func(e *zerolog.Event, v *V),
) *FileLoader[V] {
	return &FileLoader[V]{
		fp:      fp,
		parseFn: parseFn,
		logger:  logger,
		vInfo:   vInfo,
	}
}

func (s *FileLoader[V]) LoadAndStage() (ok bool) {
	b, err := os.ReadFile(s.fp)
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to read file")
		return
	}
	newHash := sha256.Sum256(b)
	if newHash == s.hash {
		s.logger.Info().Msg("skip loading file, same checksum")
		return true
	}
	v, err := s.parseFn(b)
	if v != nil {
		e := s.logger.Info()
		if s.vInfo != nil {
			s.vInfo(e, v)
		}
		e.Msg("file loaded")
		s.staged = v
		s.stagedHash = newHash
	}
	if err != nil {
		s.logger.Error().Err(err).Msg("failed to parse data")
	}
	return err == nil
}

func (s *FileLoader[V]) Init() (*V, error) {
	b, err := os.ReadFile(s.fp)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(b)
	v, err := s.parseFn(b)
	if err != nil {
		return v, err
	}
	s.v.Store(v)
	s.hash = h
	return v, nil
}

func (s *FileLoader[V]) Commit() {
	if s.staged != nil {
		s.v.Store(s.staged)
		s.hash = s.stagedHash
		s.staged = nil
		clear(s.hash[:])
	}
}

func (s *FileLoader[V]) Discard() {
	s.staged = nil
	clear(s.hash[:])
}

func (s *FileLoader[V]) V() *V {
	return s.v.Load()
}

type FileLoaderGroup[V any] []*FileLoader[V]

func (g FileLoaderGroup[V]) LoadAndStage() bool {
	for _, loader := range g {
		ok := loader.LoadAndStage()
		if !ok {
			return false
		}
	}
	return true
}

func (g FileLoaderGroup[V]) Commit() {
	for _, loader := range g {
		loader.Commit()
	}
}

func (g FileLoaderGroup[V]) Discard() {
	for _, loader := range g {
		loader.Discard()
	}
}
