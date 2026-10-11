package core

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/model/id"
	"github.com/navidrome/navidrome/model/request"
	"github.com/navidrome/navidrome/utils"
)

type Players interface {
	Get(ctx context.Context, playerId string) (*model.Player, error)
	Register(ctx context.Context, id, client, userAgent, ip string) (*model.Player, *model.Transcoding, error)
	Touch(ctx context.Context, plr model.Player, client, userAgent, ip string) (*model.Player, *model.Transcoding, error)
}

func NewPlayers(ds model.DataStore) Players {
	return &players{
		ds:      ds,
		limiter: utils.Limiter{Interval: consts.UpdatePlayerFrequency},
	}
}

type players struct {
	ds      model.DataStore
	limiter utils.Limiter
}

func (p *players) Register(ctx context.Context, playerID, client, userAgent, ip string) (*model.Player, *model.Transcoding, error) {
	var plr *model.Player
	var err error
	user, _ := request.UserFrom(ctx)
	if playerID != "" {
		plr, err = p.ds.Player().Get(ctx, playerID)
		if err == nil && (plr.Client != client || plr.UserId != user.ID) {
			playerID = ""
		}
	}
	username := userName(ctx)
	if err != nil || playerID == "" {
		plr, err = p.ds.Player().FindMatch(ctx, user.ID, client, userAgent)
		if err == nil {
			log.Debug(ctx, "Found matching player", "id", plr.ID, "client", client, "username", username, "type", userAgent)
		} else {
			plr = &model.Player{
				ID:              id.NewRandom(),
				UserId:          user.ID,
				Client:          client,
				ScrobbleEnabled: true,
				ReportRealPath:  conf.Server.Subsonic.DefaultReportRealPath,
			}
			log.Info(ctx, "Registering new player", "id", plr.ID, "client", client, "username", username, "type", userAgent)
		}
	}
	if !plr.HasAPIKey {
		plr.Name = fmt.Sprintf("%s [%s]", client, userAgent)
	}
	return p.refresh(ctx, plr, userAgent, ip)
}

// Touch refreshes a player that the request already identified (by API key), without guessing or renaming it.
func (p *players) Touch(ctx context.Context, plr model.Player, client, userAgent, ip string) (*model.Player, *model.Transcoding, error) {
	if plr.Client == "" {
		plr.Client = client
	}
	return p.refresh(ctx, &plr, userAgent, ip)
}

func (p *players) refresh(ctx context.Context, plr *model.Player, userAgent, ip string) (*model.Player, *model.Transcoding, error) {
	plr.UserAgent = userAgent
	plr.IP = ip
	plr.LastSeen = time.Now()
	p.limiter.Do(plr.ID, func() {
		ctx, cancel := context.WithTimeout(ctx, time.Second)
		defer cancel()

		if err := p.ds.Player().Put(ctx, plr); err != nil {
			log.Warn(ctx, "Could not save player", "id", plr.ID, "client", plr.Client, "username", userName(ctx), "type", plr.UserAgent, err)
		}
	})
	if plr.TranscodingId == "" {
		return plr, nil, nil
	}
	trc, err := p.ds.Transcoding().Get(ctx, plr.TranscodingId)
	if errors.Is(err, model.ErrNotFound) {
		// The referenced profile is gone. New deletes are rejected while
		// referenced, but rows orphaned before that guard existed must not keep
		// breaking playback: deterministically fall back to "no forced
		// transcoding" and synchronize the player reference with the commit.
		log.Warn(ctx, "Player references a deleted transcoding; clearing the stale reference",
			"id", plr.ID, "client", plr.Client, "transcodingId", plr.TranscodingId)
		plr.TranscodingId = ""
		if putErr := p.ds.Player().Put(ctx, plr); putErr != nil {
			log.Warn(ctx, "Could not clear stale transcoding reference from player",
				"id", plr.ID, "transcodingId", plr.TranscodingId, putErr)
		}
		return plr, nil, nil
	}
	return plr, trc, err
}

func (p *players) Get(ctx context.Context, playerId string) (*model.Player, error) {
	return p.ds.Player().Get(ctx, playerId)
}
