package trafficorchestrator

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var ErrBackendClosed = errors.New("packet backend closed")

// PacketAddress is the ABI-neutral 80-byte WinDivert address record. The
// fields are intentionally opaque to the processor; the backend owns them.
type PacketAddress struct {
	Timestamp int64
	Flags     uint32
	Reserved  uint32
	Data      [64]byte
}

type PacketBackend interface {
	Receive([]byte) (int, PacketAddress, error)
	Send([]byte, *PacketAddress) error
	Close() error
}

type EngineState string

const (
	EngineStopped  EngineState = "stopped"
	EngineStarting EngineState = "starting"
	EngineRunning  EngineState = "running"
	EngineFailed   EngineState = "failed"
)

// Engine owns exactly one PacketBackend handle and one receive loop.
type Engine struct {
	backend   PacketBackend
	processor *Processor
	logger    func(string)

	stateMu sync.Mutex
	state   EngineState
	done    chan struct{}
	runErr  error
	closed  atomic.Bool
}

func NewEngine(backend PacketBackend, processor *Processor, logger func(string)) (*Engine, error) {
	if backend == nil {
		return nil, errors.New("packet backend is required")
	}
	if processor == nil {
		return nil, errors.New("packet processor is required")
	}
	return &Engine{backend: backend, processor: processor, logger: logger, state: EngineStopped}, nil
}

func (e *Engine) Start() error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	if e.closed.Load() {
		return ErrBackendClosed
	}
	if e.state != EngineStopped {
		return fmt.Errorf("engine cannot start from state %s", e.state)
	}
	e.state = EngineStarting
	e.done = make(chan struct{})
	e.runErr = nil
	go e.run()
	e.state = EngineRunning
	e.log(fmt.Sprintf("traffic engine started with plan revision %d", e.processor.Revision()))
	return nil
}

func (e *Engine) ApplyPlan(plan TrafficPlan) error {
	if e == nil || e.processor == nil {
		return errors.New("engine is not initialized")
	}
	if err := e.processor.ApplyPlan(plan); err != nil {
		return err
	}
	e.log(fmt.Sprintf("traffic plan revision %d applied atomically", plan.Revision))
	return nil
}

func (e *Engine) State() EngineState {
	if e == nil {
		return EngineStopped
	}
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.state
}

func (e *Engine) Wait() error {
	if e == nil {
		return nil
	}
	e.stateMu.Lock()
	done := e.done
	e.stateMu.Unlock()
	if done == nil {
		return nil
	}
	<-done
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	return e.runErr
}

func (e *Engine) Stop() error {
	if e == nil {
		return nil
	}
	if e.closed.Swap(true) {
		return nil
	}
	closeErr := e.backend.Close()
	e.stateMu.Lock()
	done := e.done
	e.stateMu.Unlock()
	if done != nil {
		<-done
		e.stateMu.Lock()
		runErr := e.runErr
		e.stateMu.Unlock()
		if runErr != nil && !errors.Is(runErr, ErrBackendClosed) && closeErr == nil {
			closeErr = runErr
		}
	}
	e.stateMu.Lock()
	e.state = EngineStopped
	e.stateMu.Unlock()
	e.log("traffic engine stopped")
	return closeErr
}

func (e *Engine) run() {
	buffer := make([]byte, 65535)
	var runErr error
	defer func() {
		e.stateMu.Lock()
		e.runErr = runErr
		if runErr != nil && !errors.Is(runErr, ErrBackendClosed) {
			e.state = EngineFailed
		} else if e.state != EngineStopped {
			e.state = EngineStopped
		}
		e.stateMu.Unlock()
		close(e.done)
	}()
	for {
		length, address, err := e.backend.Receive(buffer)
		if err != nil {
			runErr = err
			return
		}
		if length <= 0 || length > len(buffer) {
			continue
		}
		original := append([]byte(nil), buffer[:length]...)
		decision := e.processor.Process(original)
		for index, packet := range decision.Packets {
			if err := e.backend.Send(packet, &address); err != nil {
				// The original packet is the only safe fallback. Try it once if a
				// synthetic/segmented send failed before ending the engine.
				if index < len(decision.Packets)-1 || decision.Transformed {
					_ = e.backend.Send(original, &address)
				}
				runErr = fmt.Errorf("send packet: %w", err)
				return
			}
		}
	}
}

func (e *Engine) log(message string) {
	if e != nil && e.logger != nil {
		e.logger(message)
	}
}
