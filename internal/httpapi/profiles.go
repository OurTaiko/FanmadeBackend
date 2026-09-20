package httpapi

import (
	"context"
	"time"
)

// Public catalogs stay available during an identity-provider outage. Account
// checks never use this fallback; they always require a live SSO response.
func (s *Server) publicNames(ctx context.Context, ids []string) map[string]string {
	result := map[string]string{}
	unique := []string{}
	for _, id := range ids {
		if _, ok := result[id]; !ok {
			unique = append(unique, id)
			result[id] = "未知用户"
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
	names := s.publicNames(ctx, ids)
	for i := range charts {
		charts[i].Uploader = names[charts[i].OwnerID]
	}
}
