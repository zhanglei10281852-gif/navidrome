package tests

import (
	"context"

	"github.com/navidrome/navidrome/model"
)

type MockTranscodingRepo struct {
	model.TranscodingRepository
	// Error, when set, is returned by every lookup.
	Error error
	// Data optionally overrides the built-in canned rows. Keys are target
	// formats; nil Data keeps the historical canned responses.
	Data map[string]*model.Transcoding
	// ByID optionally overrides Get lookups by transcoding id.
	ByID map[string]*model.Transcoding
	// Players counts references reported by CountPlayers. When nil, zero is
	// returned for every id.
	Players map[string]int64
}

func (m *MockTranscodingRepo) Get(_ context.Context, id string) (*model.Transcoding, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	if m.ByID != nil {
		if t, ok := m.ByID[id]; ok {
			cp := *t
			return &cp, nil
		}
		return nil, model.ErrNotFound
	}
	return &model.Transcoding{ID: id, TargetFormat: "mp3", DefaultBitRate: 160}, nil
}

func (m *MockTranscodingRepo) FindByFormat(_ context.Context, format string) (*model.Transcoding, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	if m.Data != nil {
		if t, ok := m.Data[format]; ok {
			cp := *t
			return &cp, nil
		}
		return nil, model.ErrNotFound
	}
	switch format {
	case "mp3":
		return &model.Transcoding{ID: "mp31", TargetFormat: "mp3", DefaultBitRate: 160}, nil
	case "oga":
		return &model.Transcoding{ID: "oga1", TargetFormat: "oga", DefaultBitRate: 128}, nil
	case "opus":
		return &model.Transcoding{ID: "opus1", TargetFormat: "opus", DefaultBitRate: 96}, nil
	case "flac":
		return &model.Transcoding{ID: "flac1", TargetFormat: "flac", DefaultBitRate: 0, Command: "ffmpeg -ss %t -i %s -map 0:a:0 -v 0 -c:a flac -f flac -"}, nil
	case "aac":
		return &model.Transcoding{ID: "aac1", TargetFormat: "aac", DefaultBitRate: 256, Command: "ffmpeg -ss %t -i %s -map 0:a:0 -b:a %bk -v 0 -c:a aac -f ipod -movflags frag_keyframe+empty_moov -"}, nil
	default:
		return nil, model.ErrNotFound
	}
}

// All returns every configured row with its Command field intact.
func (m *MockTranscodingRepo) All(_ context.Context) (model.Transcodings, error) {
	if m.Error != nil {
		return nil, m.Error
	}
	if m.Data != nil {
		res := make(model.Transcodings, 0, len(m.Data))
		for _, t := range m.Data {
			res = append(res, *t)
		}
		return res, nil
	}
	return model.Transcodings{
		{ID: "mp31", TargetFormat: "mp3", DefaultBitRate: 160},
		{ID: "opus1", TargetFormat: "opus", DefaultBitRate: 96},
		{ID: "flac1", TargetFormat: "flac", DefaultBitRate: 0, Command: "ffmpeg -ss %t -i %s -map 0:a:0 -v 0 -c:a flac -f flac -"},
		{ID: "aac1", TargetFormat: "aac", DefaultBitRate: 256, Command: "ffmpeg -ss %t -i %s -map 0:a:0 -b:a %bk -v 0 -c:a aac -f ipod -movflags frag_keyframe+empty_moov -"},
	}, nil
}

func (m *MockTranscodingRepo) CountPlayers(_ context.Context, transcodingID string) (int64, error) {
	if m.Error != nil {
		return 0, m.Error
	}
	return m.Players[transcodingID], nil
}
