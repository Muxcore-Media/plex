package internal

import (
	"context"

	plexv1 "github.com/Muxcore-Media/plex/proto/plexv1"
)

func (m *Module) ListSyncLists(ctx context.Context, req *plexv1.ListSyncListsRequest) (*plexv1.ListSyncListsResponse, error) {
	if !m.configured() {
		return &plexv1.ListSyncListsResponse{}, nil
	}
	if req.GetRefresh() {
		if err := m.refreshSyncLists(ctx); err != nil {
			return nil, err
		}
	}
	snap := m.cachedSyncLists(req.GetUserId(), req.GetClientId())
	if snap.UpdatedAt == "" {
		if err := m.refreshSyncLists(ctx); err != nil {
			return nil, err
		}
		snap = m.cachedSyncLists(req.GetUserId(), req.GetClientId())
	}
	return syncListsToProto(snap), nil
}

func syncListsToProto(snap syncListsSnapshot) *plexv1.ListSyncListsResponse {
	out := &plexv1.ListSyncListsResponse{
		MachineIdentifier: snap.MachineIdentifier,
		UpdatedAt:         snap.UpdatedAt,
	}
	for _, list := range snap.Lists {
		msg := &plexv1.PlexSyncListMessage{
			Id:               list.ID,
			ClientIdentifier: list.ClientIdentifier,
			DeviceUserId:     list.DeviceUserID,
			DeviceName:       list.DeviceName,
			DevicePlatform:   list.DevicePlatform,
			DeviceProduct:    list.DeviceProduct,
		}
		for _, item := range list.Items {
			msg.Items = append(msg.Items, &plexv1.PlexSyncItemMessage{
				Id:                   item.ID,
				Title:                item.Title,
				RootTitle:            item.RootTitle,
				MetadataType:         item.MetadataType,
				ContentType:          item.ContentType,
				MediaType:            item.MediaType,
				RatingKey:            item.RatingKey,
				State:                item.State,
				Failure:              item.Failure,
				ItemsCount:           int32(item.ItemsCount),           //nolint:gosec // Plex sync counts fit int32 proto fields
				ItemsCompleteCount:   int32(item.ItemsCompleteCount),   //nolint:gosec // Plex sync counts fit int32 proto fields
				ItemsDownloadedCount: int32(item.ItemsDownloadedCount), //nolint:gosec // Plex sync counts fit int32 proto fields
				TotalSizeBytes:       item.TotalSizeBytes,
				VideoResolution:      item.VideoResolution,
			})
		}
		out.Lists = append(out.Lists, msg)
	}
	return out
}
