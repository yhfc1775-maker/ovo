package repo

import (
	"errors"

	"go-backend/internal/store/model"
)

// ListKernelNodes reads only the metadata displayed by kernelViews. The ID set
// can span every node on the panel, so every query uses the shared IN bound.
func (r *Repository) ListKernelNodes(ids []int64) (map[int64]*model.Node, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("repository not initialized")
	}
	nodes := make(map[int64]*model.Node, len(ids))
	for _, chunk := range chunkInt64IDs(ids) {
		var rows []model.Node
		if err := r.db.Select("id", "name", "status").Where("id IN ?", chunk).Find(&rows).Error; err != nil {
			return nil, err
		}
		for i := range rows {
			nodes[rows[i].ID] = &rows[i]
		}
	}
	return nodes, nil
}
