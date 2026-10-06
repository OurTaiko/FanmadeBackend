package httpapi

import (
	"context"
	"time"
)

// Public catalogs stay available during an identity-provider outage. Account
// checks never use this fallback; they always require a live SSO response.
func (s *Server) publicProfiles(ctx context.Context, ids []string) map[string]PublicProfile {
	result := map[string]PublicProfile{}
	unique := []string{}
	for _, id := range ids {
		if _, ok := result[id]; !ok {
			unique = append(unique, id)
			result[id] = PublicProfile{Nickname: "未知用户"}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	names, e := s.Config.SSO.Profiles(ctx, unique)
	if e == nil {
		for id, name := range names {
			result[id] = name
		}
	}
	return result
}
func (s *Server) chartNames(ctx context.Context, charts []Chart) {
	ids := make([]string, len(charts))
	for i, c := range charts {
		ids[i] = c.OwnerID
	}
	profiles := s.publicProfiles(ctx, ids)
	for i := range charts {
		charts[i].setUploader(profiles[charts[i].OwnerID])
	}
}

func (c *Chart) setUploader(p PublicProfile) {
	c.Uploader = p.Nickname
	c.UploaderAvatarURL = p.AvatarURL
}
