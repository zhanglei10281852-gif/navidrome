package tests

import (
	"github.com/deluan/rest"
	"github.com/navidrome/navidrome/model"
)

type MockShareRepo struct {
	model.ShareRepository
	rest.Repository
	rest.Persistable

	Entity any
	ID     string
	Cols   []string
	Error  error

	Epoch            int
	RotateCalled     bool
	RotateError      error
	SettleMiss       bool
	SettleError      error
	SettleCalledWith string
	SettleCallCount  int
}

func (m *MockShareRepo) Save(entity any) (string, error) {
	if m.Error != nil {
		return "", m.Error
	}
	s := entity.(*model.Share)
	if s.ID == "" {
		s.ID = "id"
	}
	m.Entity = s
	return s.ID, nil
}

func (m *MockShareRepo) Update(id string, entity any, cols ...string) error {
	if m.Error != nil {
		return m.Error
	}
	m.ID = id
	m.Entity = entity
	m.Cols = cols
	return nil
}

func (m *MockShareRepo) Exists(id string) (bool, error) {
	if m.Error != nil {
		return false, m.Error
	}
	return id == m.ID, nil
}

func (m *MockShareRepo) Get(id string) (*model.Share, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	if id != m.ID {
		return nil, model.ErrNotFound
	}
	if s, ok := m.Entity.(*model.Share); ok {
		return s, nil
	}
	return &model.Share{ID: id}, nil
}

func (m *MockShareRepo) RotateEpoch(id string) (int, error) {
	m.RotateCalled = true
	if m.RotateError != nil {
		return 0, m.RotateError
	}
	m.Epoch++
	if s, ok := m.Entity.(*model.Share); ok {
		s.Epoch = m.Epoch
	}
	return m.Epoch, nil
}

func (m *MockShareRepo) SettleDownload(id string) (bool, error) {
	m.SettleCallCount++
	m.SettleCalledWith = id
	if m.SettleError != nil {
		return false, m.SettleError
	}
	if m.SettleMiss {
		return false, nil
	}
	if s, ok := m.Entity.(*model.Share); ok {
		s.DownloadCount++
	}
	return true, nil
}
