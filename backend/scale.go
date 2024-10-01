package main

import (
	"encoding/json"
	"fmt"
	"github.com/sirupsen/logrus"
	"sync"
	"time"
)

const OkLimit = 5 * time.Minute

type Measurement struct {
	Index  int       `json:"index"`
	Weight float64   `json:"weight"`
	At     time.Time `json:"at"`
}

type Pub struct {
	IsOpen   bool      `json:"is_open"`
	OpenedAt time.Time `json:"open_at"`
	ClosedAt time.Time `json:"closed_at"`
}

type Scale struct {
	mux     sync.Mutex
	monitor *Monitor

	Measurements []Measurement `json:"measurements"`
	index        int
	size         int
	valid        int // number of valid measurements

	Pub       Pub `json:"pub"`
	ActiveKeg int `json:"active_keg"`

	LastOk time.Time `json:"last_ok"`
	Rssi   float64   `json:"rssi"`

	store  Storage
	logger *logrus.Logger
}

func NewScale(bufferSize int, monitor *Monitor, store Storage, logger *logrus.Logger) *Scale {
	s := &Scale{
		mux:     sync.Mutex{},
		monitor: monitor,

		Measurements: make([]Measurement, bufferSize),
		index:        -1,
		size:         bufferSize,
		valid:        0,

		Pub: Pub{
			IsOpen:   false,
			OpenedAt: time.Now().Add(-9999 * time.Hour),
			ClosedAt: time.Now().Add(-9999 * time.Hour),
		},
		ActiveKeg: 0,

		LastOk: time.Now().Add(-9999 * time.Hour),

		store:  store,
		logger: logger,
	}

	s.loadDataFromStore()

	// periodically call recheck
	go func(s *Scale) {
		for {
			time.Sleep(15 * time.Second)
			s.Recheck()
		}
		// @todo - I don't really care about cancellation right now
	}(s)

	return s
}

func (s *Scale) loadDataFromStore() {
	measurements, err := s.store.GetMeasurements()
	if err == nil {
		s.index = len(measurements) - 1
		i := 0
		for _, m := range measurements {
			s.Measurements[i] = m
		}
	}

	activeKeg, err := s.store.GetActiveKeg()
	if err == nil {
		s.ActiveKeg = activeKeg
	}
}

func (s *Scale) AddMeasurement(weight float64) error {
	if weight < 6000 || weight > 65000 {
		s.logger.Infof("Invalid weight: %f", weight)
		return nil
	}

	s.monitor.kegWeight.WithLabelValues().Set(weight)

	s.mux.Lock()
	defer s.mux.Unlock()

	s.index++
	if s.index >= len(s.Measurements) {
		s.index = 0
	}

	m := Measurement{
		Index:  s.index,
		Weight: weight,
		At:     time.Now(),
	}

	s.Measurements[s.index] = m
	err := s.store.AddMeasurement(m)
	if err != nil {
		return fmt.Errorf("could not store measurement: %w", err)
	}

	if s.valid < s.size {
		s.valid++
	}

	return nil
}

func (s *Scale) GetLastMeasurement() Measurement {
	return s.GetMeasurement(0)
}

// GetMeasurement GetValidCount return number of valid measurements
func (s *Scale) GetMeasurement(index int) Measurement {
	if index > s.GetValidCount() || index > s.size {
		return Measurement{
			Weight: 0,
			Index:  -1,
		}
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	idx := (s.index - index + s.size) % s.size
	return s.Measurements[idx]
}

func (s *Scale) JsonState() ([]byte, error) {
	s.mux.Lock()
	defer s.mux.Unlock()

	return json.Marshal(s)
}

func (s *Scale) Ping() {
	s.monitor.lastUpdate.WithLabelValues().SetToCurrentTime()

	s.mux.Lock()
	defer s.mux.Unlock()

	if !s.Pub.IsOpen {
		s.monitor.pubIsOpen.WithLabelValues().Set(1)
		s.Pub.IsOpen = true
		s.Pub.OpenedAt = time.Now()
	}

	s.LastOk = time.Now()
}

// Recheck sets the scale to not open
// it should be called everytime we want to get some calculations
// to recalculate the state of the scale
func (s *Scale) Recheck() {
	ok := s.IsOk() // mutex

	if ok {
		return
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	if s.Pub.IsOpen { // we haven't received any data for [OkLimit] minutes and pub is open
		s.monitor.pubIsOpen.WithLabelValues().Set(0)
		s.Pub.IsOpen = false
		s.Pub.ClosedAt = time.Now().Add(-1 * OkLimit)
	}
}

func (s *Scale) IsOk() bool {
	s.mux.Lock()
	defer s.mux.Unlock()

	return time.Since(s.LastOk) < OkLimit
}

func (s *Scale) SetRssi(rssi float64) {
	s.monitor.scaleWifiRssi.WithLabelValues().Set(rssi)

	s.mux.Lock()
	defer s.mux.Unlock()

	s.Rssi = rssi
}

// GetValidCount returns the number of valid measurements
func (s *Scale) GetValidCount() int {
	s.mux.Lock()
	defer s.mux.Unlock()

	return s.valid
}

// HasLastN returns true if the last n measurements are not empty
func (s *Scale) HasLastN(n int) bool {
	if n > s.size {
		n = s.size
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	return s.valid >= n
}

// SumLastN returns the sum of the last n measurements
func (s *Scale) SumLastN(n int) float64 {
	if n > s.size {
		n = s.size
	}

	s.mux.Lock()
	defer s.mux.Unlock()

	sum := 0.0
	for i := 0; i < n; i++ {
		idx := (s.index - i + s.size) % s.size
		sum += s.Measurements[idx].Weight
	}

	return sum
}

// AvgLastN returns the average of the last n measurements
// It ignores empty measurements - you should call HasLastN before calling this
func (s *Scale) AvgLastN(n int) float64 {
	if n > s.size {
		n = s.size
	}

	if n == 0 {
		return 0
	}

	return s.SumLastN(n) / float64(n)
}

func (s *Scale) SetActiveKeg(keg int) error {
	s.mux.Lock()
	defer s.mux.Unlock()

	s.ActiveKeg = keg
	return s.store.SetActiveKeg(keg)
}
