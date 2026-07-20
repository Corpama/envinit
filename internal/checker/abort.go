package checker

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
)

var (
	errCheckItemAborted  = errors.New("test item aborted by user")
	errCheckStageAborted = errors.New("check stage aborted by user")
)

type checkAbortManager struct {
	mu            sync.Mutex
	stageState    map[string]string
	stageCancel   map[string]context.CancelCauseFunc
	itemCancel    map[string]context.CancelCauseFunc
	abortedStages map[string]bool
	abortedItems  map[string]bool
}

func newCheckAbortManager(stages []string) *checkAbortManager {
	manager := &checkAbortManager{
		stageState:    map[string]string{},
		stageCancel:   map[string]context.CancelCauseFunc{},
		itemCancel:    map[string]context.CancelCauseFunc{},
		abortedStages: map[string]bool{},
		abortedItems:  map[string]bool{},
	}
	for _, stage := range stages {
		manager.stageState[stage] = "pending"
	}
	return manager
}

func abortItemKey(stage, id string) string { return stage + "\x00" + id }

func (m *checkAbortManager) beginStage(opts Options, stage string) (Options, func()) {
	ctx, cancel := context.WithCancelCause(checkContext(opts))
	m.mu.Lock()
	m.stageState[stage] = "active"
	m.stageCancel[stage] = cancel
	aborted := m.abortedStages[stage]
	m.mu.Unlock()
	if aborted {
		cancel(errCheckStageAborted)
	}
	stageOpts := opts
	stageOpts.Context = ctx
	return stageOpts, func() {
		m.mu.Lock()
		delete(m.stageCancel, stage)
		m.stageState[stage] = "done"
		m.mu.Unlock()
		cancel(nil)
	}
}

func (m *checkAbortManager) beginItem(opts Options, stage, id string) (Options, func()) {
	ctx, cancel := context.WithCancelCause(checkContext(opts))
	key := abortItemKey(stage, id)
	m.mu.Lock()
	m.itemCancel[key] = cancel
	aborted := m.abortedStages[stage] || m.abortedItems[key]
	m.mu.Unlock()
	if aborted {
		cancel(errCheckItemAborted)
	}
	itemOpts := opts
	itemOpts.Context = ctx
	return itemOpts, func() {
		m.mu.Lock()
		delete(m.itemCancel, key)
		m.mu.Unlock()
		cancel(nil)
	}
}

func (m *checkAbortManager) beginRetestItem(opts Options, stage, id string) (Options, func()) {
	ctx, cancel := context.WithCancelCause(checkContext(opts))
	key := abortItemKey(stage, id)
	m.mu.Lock()
	delete(m.abortedItems, key)
	m.itemCancel[key] = cancel
	m.mu.Unlock()
	itemOpts := opts
	itemOpts.Context = ctx
	return itemOpts, func() {
		m.mu.Lock()
		delete(m.itemCancel, key)
		m.mu.Unlock()
		cancel(nil)
	}
}

func (m *checkAbortManager) abortItem(stage, id string) bool {
	if m == nil || stage == "" || id == "" {
		return false
	}
	key := abortItemKey(stage, id)
	m.mu.Lock()
	cancel := m.itemCancel[key]
	if m.stageState[stage] == "done" && cancel == nil {
		m.mu.Unlock()
		return false
	}
	m.abortedItems[key] = true
	m.mu.Unlock()
	if cancel != nil {
		cancel(errCheckItemAborted)
	}
	return true
}

func (m *checkAbortManager) abortStage(stage string) bool {
	if m == nil || stage == "" {
		return false
	}
	m.mu.Lock()
	if m.stageState[stage] == "done" {
		m.mu.Unlock()
		return false
	}
	m.abortedStages[stage] = true
	cancel := m.stageCancel[stage]
	m.mu.Unlock()
	if cancel != nil {
		cancel(errCheckStageAborted)
	}
	return true
}

func (m *checkAbortManager) failures() []string {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var failures []string
	for stage := range m.abortedStages {
		failures = append(failures, fmt.Sprintf("%s stage aborted by user", stage))
	}
	for key := range m.abortedItems {
		stage, id := splitAbortItemKey(key)
		if !m.abortedStages[stage] {
			failures = append(failures, fmt.Sprintf("%s item %s aborted by user", stage, id))
		}
	}
	sort.Strings(failures)
	return failures
}

func splitAbortItemKey(key string) (string, string) {
	for index := range key {
		if key[index] == 0 {
			return key[:index], key[index+1:]
		}
	}
	return key, ""
}

func beginCheckItem(opts Options, stage, id string) (Options, func()) {
	if opts.aborts == nil {
		return opts, func() {}
	}
	return opts.aborts.beginItem(opts, stage, id)
}

func beginRetestCheckItem(opts Options, stage, id string) (Options, func()) {
	if opts.aborts == nil {
		return opts, func() {}
	}
	return opts.aborts.beginRetestItem(opts, stage, id)
}
