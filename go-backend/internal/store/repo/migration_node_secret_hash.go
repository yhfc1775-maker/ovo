package repo

import (
	"fmt"

	"go-backend/internal/security"
	"go-backend/internal/store/model"

	"gorm.io/gorm"
)

// migrateNodeSecretHashes 是 F03 的存量明文 secret 一次性哈希迁移：
// 把 secret 非空、secret_hash 为空的节点行逐行哈希（HashNodeSecret）
// 后清零明文列。幂等（只处理未迁移行，无符合行即 no-op），启动时由
// autoMigrateAll 在双后端调用；节点数小（面板节点表），全表扫描一次
// 的成本可忽略。迁移失败即启动失败（fail-closed，不带明文 secret 跑
// 新代码——否则新代码仍会按 legacy 回退面认证，明文列继续留存）。
//
// 不破坏已在线节点的认证：迁移只改存储形态，agent 出示的明文原值
// 不变，GetNodeBySecret 对出示值哈希比对，迁移前后均命中。
func migrateNodeSecretHashes(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("migrateNodeSecretHashes: nil db")
	}
	if !db.Migrator().HasTable(&model.Node{}) {
		return nil
	}
	if !db.Migrator().HasColumn(&model.Node{}, "secret_hash") {
		return fmt.Errorf("migrateNodeSecretHashes: node.secret_hash column missing (AutoMigrate did not add it)")
	}
	var rows []struct {
		ID     int64
		Secret string
	}
	if err := db.Model(&model.Node{}).Select("id", "secret").
		Where("secret <> '' AND (secret_hash IS NULL OR secret_hash = '')").
		Find(&rows).Error; err != nil {
		return fmt.Errorf("migrateNodeSecretHashes: list legacy rows: %w", err)
	}
	for _, row := range rows {
		if err := db.Model(&model.Node{}).Where("id = ?", row.ID).Updates(map[string]interface{}{
			"secret":      "",
			"secret_hash": security.HashNodeSecret(row.Secret),
		}).Error; err != nil {
			return fmt.Errorf("migrateNodeSecretHashes: migrate node #%d: %w", row.ID, err)
		}
	}
	return nil
}
