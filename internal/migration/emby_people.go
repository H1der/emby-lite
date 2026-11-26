package migration

import (
	"context"
	"fmt"
	"strings"
	"time"

	"emby-lite/internal/model"
	"emby-lite/internal/scanner"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// personBatchSize is how many items' people links are written in one transaction.
const personBatchSize = 500

type embyPersonLink struct {
	Name      string
	RoleType  string
	RoleName  string
	SortIdx   int
	TMDBID    string
	ThumbPath string
}

type embyPersonGroup struct {
	EmbyID  string
	LocalID string
	Links   []embyPersonLink
}

// embyPersonRoles maps Emby's numeric PersonType enum and the textual form
// used by some older schemas to the canonical PersonDto type names.
var embyPersonRoles = map[string]string{
	"0": "Actor", "actor": "Actor",
	"1": "Director", "director": "Director",
	"2": "Writer", "writer": "Writer",
	"3": "Producer", "producer": "Producer",
	"4": "GuestStar", "gueststar": "GuestStar",
	"5": "Composer", "composer": "Composer",
	"6": "Conductor", "conductor": "Conductor",
	"7": "Lyricist", "lyricist": "Lyricist",
	"8": "Arranger", "arranger": "Arranger",
	"9": "Engineer", "engineer": "Engineer",
	"10": "Mixer", "mixer": "Mixer",
	"11": "Performer", "performer": "Performer",
	"12": "Author", "author": "Author",
	"13": "Illustrator", "illustrator": "Illustrator",
	"14": "Editor", "editor": "Editor",
	"15": "Translator", "translator": "Translator",
}

// embyPersonRole returns the canonical role type for an Emby PersonType value.
// Unknown non-empty types are kept as Other instead of dropping the person.
func embyPersonRole(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if role, ok := embyPersonRoles[strings.ToLower(raw)]; ok {
		return role
	}
	return "Other"
}

// embyPersonRoleOrder keeps performers at the front of the detail page and
// pushes directors, writers and other crew behind them.
const embyPersonRoleOrder = `CASE LOWER(CAST(ip.PersonType AS TEXT))
	WHEN '0' THEN 0 WHEN 'actor' THEN 0
	WHEN '4' THEN 1 WHEN 'gueststar' THEN 1
	WHEN '1' THEN 2 WHEN 'director' THEN 2
	WHEN '2' THEN 3 WHEN 'writer' THEN 3
	WHEN '3' THEN 4 WHEN 'producer' THEN 4
	WHEN '5' THEN 5 WHEN 'composer' THEN 5
	WHEN '6' THEN 6 WHEN 'conductor' THEN 6
	WHEN '7' THEN 7 WHEN 'lyricist' THEN 7
	WHEN '8' THEN 8 WHEN 'arranger' THEN 8
	WHEN '9' THEN 9 WHEN 'engineer' THEN 9
	WHEN '10' THEN 10 WHEN 'mixer' THEN 10
	WHEN '11' THEN 11 WHEN 'performer' THEN 11
	WHEN '12' THEN 12 WHEN 'author' THEN 12
	WHEN '13' THEN 13 WHEN 'illustrator' THEN 13
	WHEN '14' THEN 14 WHEN 'editor' THEN 14
	WHEN '15' THEN 15 WHEN 'translator' THEN 15
	ELSE 99
END`

// embyPersonThumb extracts a usable portrait URL from Emby's Images column.
// Emby stores local metadata paths with a %MetadataPath% placeholder; those are
// only valid inside the old Emby config tree, so they are ignored here and the
// request-time lookup by person name/TMDB id takes over.
func embyPersonThumb(raw string) string {
	for _, entry := range strings.Split(raw, "|") {
		parts := strings.SplitN(strings.TrimSpace(entry), "*", 4)
		if len(parts) < 3 {
			if u := strings.TrimSpace(parts[0]); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
				return u
			}
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(parts[2]), "Primary") {
			continue
		}
		if u := strings.TrimSpace(parts[0]); strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
			return u
		}
	}
	return ""
}

func embyPersonMeta(providerIDs, images string) (tmdbID, thumb string) {
	tmdbID = strings.TrimSpace(parseEmbyProviderIDs(providerIDs)["Tmdb"])
	return tmdbID, embyPersonThumb(images)
}

// importEmbyPeople streams ItemPeople2 and writes the cast/crew rows for items
// that were just migrated. It deliberately does not load all links into memory:
// a real Emby library holds hundreds of thousands of rows.
//
// When overwrite is false, only links that are still missing are inserted.
// That makes a re-run able to repair an earlier import (including one that
// only knew Actor/Director/Writer) without deleting or overwriting existing
// metadata or scanner-owned people rows.
func (s *Service) importEmbyPeople(ctx context.Context, libraryDBPath string, targets map[string]string, overwrite bool, now time.Time) (int, []string, error) {
	if len(targets) == 0 || strings.TrimSpace(libraryDBPath) == "" {
		return 0, nil, nil
	}

	sqlDB, err := openReadOnlySQLite(libraryDBPath)
	if err != nil {
		return 0, nil, fmt.Errorf("open Emby library.db for people: %w", err)
	}
	defer sqlDB.Close()

	linkTable := ""
	for _, candidate := range []string{"ItemPeople2", "ItemPeople"} {
		if len(tableColumns(sqlDB, candidate)) > 0 {
			linkTable = candidate
			break
		}
	}
	if linkTable == "" {
		return 0, []string{"library.db 中未找到 ItemPeople2/ItemPeople，未能迁移演职人员"}, nil
	}
	linkCols := tableColumns(sqlDB, linkTable)
	for _, required := range []string{"itemid", "personid", "persontype"} {
		if !linkCols[required] {
			return 0, []string{linkTable + " 表缺少必要字段，未能迁移演职人员"}, nil
		}
	}

	personTable := "MediaItems"
	personCols := tableColumns(sqlDB, personTable)
	if len(personCols) == 0 {
		personTable = "TypedBaseItems"
		personCols = tableColumns(sqlDB, personTable)
	}
	if len(personCols) == 0 {
		return 0, []string{"library.db 中未找到 MediaItems 或 TypedBaseItems 表，未能迁移演职人员"}, nil
	}

	nameExpr := "COALESCE(p.Name, '')"
	if !personCols["name"] {
		nameExpr = "''"
	}
	providerExpr := "''"
	if personCols["providerids"] {
		providerExpr = "COALESCE(CAST(p.ProviderIds AS TEXT), '')"
	}
	imagesExpr := "''"
	if personCols["images"] {
		imagesExpr = "COALESCE(CAST(p.Images AS TEXT), '')"
	}
	roleExpr := "''"
	if linkCols["role"] {
		roleExpr = "COALESCE(CAST(ip.Role AS TEXT), '')"
	}
	orderExpr := "0"
	if linkCols["listorder"] {
		orderExpr = "COALESCE(ip.ListOrder, 0)"
	}

	personKey := "Id"
	personIDExpr := "CAST(p.Id AS TEXT)"
	if !personCols["id"] && personCols["guid"] {
		personKey = "guid"
		personIDExpr = "CAST(p.guid AS TEXT)"
	}

	q := fmt.Sprintf(`SELECT CAST(ip.ItemId AS TEXT), %s, %s, %s,
		COALESCE(CAST(ip.PersonType AS TEXT), ''), %s, %s, %s
		FROM %s ip JOIN %s p ON p.%s = ip.PersonId
		ORDER BY ip.ItemId,
			%s,
			%s, ip.PersonId`,
		personIDExpr, nameExpr, roleExpr, orderExpr, providerExpr, imagesExpr,
		linkTable, personTable, personKey, embyPersonRoleOrder, orderExpr)

	rows, err := sqlDB.Query(q)
	if err != nil {
		return 0, []string{"读取演职人员失败: " + err.Error()}, nil
	}
	defer rows.Close()

	var batch []embyPersonGroup
	var current *embyPersonGroup
	seenLinks := map[string]bool{}
	imported := 0

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		n, writeErr := s.writePeopleBatch(batch, overwrite, now)
		if writeErr != nil {
			return writeErr
		}
		imported += n
		batch = batch[:0]
		return nil
	}

	for rows.Next() {
		var itemEmbyID, personEmbyID, name, roleName, rawRoleType, rawPids, rawImages string
		var listOrder int
		if err := rows.Scan(&itemEmbyID, &personEmbyID, &name, &roleName, &rawRoleType, &listOrder, &rawPids, &rawImages); err != nil {
			continue
		}

		localID, ok := targets[itemEmbyID]
		if !ok {
			continue
		}
		roleType := embyPersonRole(rawRoleType)
		name = strings.TrimSpace(name)
		if roleType == "" || name == "" {
			continue
		}

		if current == nil || current.EmbyID != itemEmbyID {
			if current != nil {
				batch = append(batch, *current)
				current = nil
				if len(batch) >= personBatchSize {
					if err := flush(); err != nil {
						return imported, nil, err
					}
				}
			}
			if err := ctx.Err(); err != nil {
				return imported, nil, err
			}
			current = &embyPersonGroup{EmbyID: itemEmbyID, LocalID: localID}
			seenLinks = map[string]bool{}
		}

		key := personEmbyID + "|" + roleType
		if seenLinks[key] {
			continue
		}
		seenLinks[key] = true

		tmdbID, thumb := embyPersonMeta(rawPids, rawImages)
		current.Links = append(current.Links, embyPersonLink{
			Name:      name,
			RoleType:  roleType,
			RoleName:  strings.TrimSpace(roleName),
			SortIdx:   len(current.Links),
			TMDBID:    tmdbID,
			ThumbPath: thumb,
		})
	}
	if err := rows.Err(); err != nil {
		return imported, nil, err
	}
	if current != nil {
		batch = append(batch, *current)
	}
	if err := flush(); err != nil {
		return imported, nil, err
	}
	return imported, nil, nil
}

func (s *Service) writePeopleBatch(groups []embyPersonGroup, overwrite bool, now time.Time) (int, error) {
	if len(groups) == 0 {
		return 0, nil
	}

	localIDs := make([]string, 0, len(groups))
	for _, g := range groups {
		localIDs = append(localIDs, g.LocalID)
	}

	// Without overwrite, fill gaps link by link so an earlier import that only
	// knew Actor/Director/Writer can gain the remaining roles on a re-run.
	existingLinks := map[string]bool{}
	if !overwrite {
		var existing []model.ItemPerson
		if err := s.db.Gorm.Model(&model.ItemPerson{}).
			Select("item_id", "person_id", "role_type").
			Where("item_id IN ?", localIDs).
			Find(&existing).Error; err != nil {
			return 0, err
		}
		for _, link := range existing {
			existingLinks[link.ItemID+"|"+link.PersonID+"|"+link.RoleType] = true
		}
	}

	var people []model.Person
	personIndex := map[string]int{}
	var links []model.ItemPerson
	var deleteIDs []string
	for _, g := range groups {
		if overwrite {
			deleteIDs = append(deleteIDs, g.LocalID)
		}
		for _, l := range g.Links {
			pid := scanner.StableID("person", strings.ToLower(l.Name))
			linkKey := g.LocalID + "|" + pid + "|" + l.RoleType
			if existingLinks[linkKey] {
				continue
			}
			existingLinks[linkKey] = true
			if idx, ok := personIndex[pid]; ok {
				if people[idx].TMDBID == "" {
					people[idx].TMDBID = l.TMDBID
				}
				if people[idx].ThumbPath == "" {
					people[idx].ThumbPath = l.ThumbPath
				}
			} else {
				personIndex[pid] = len(people)
				people = append(people, model.Person{
					ID:        pid,
					Name:      l.Name,
					TMDBID:    l.TMDBID,
					ThumbPath: l.ThumbPath,
					UpdatedAt: now,
				})
			}
			links = append(links, model.ItemPerson{
				ItemID:   g.LocalID,
				PersonID: pid,
				RoleType: l.RoleType,
				RoleName: l.RoleName,
				SortIdx:  l.SortIdx,
			})
		}
	}
	if len(links) == 0 {
		return 0, nil
	}

	err := s.db.Gorm.Transaction(func(tx *gorm.DB) error {
		if len(deleteIDs) > 0 {
			if err := tx.Where("item_id IN ?", deleteIDs).Delete(&model.ItemPerson{}).Error; err != nil {
				return err
			}
		}
		if len(people) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "id"}},
				DoUpdates: clause.Assignments(map[string]any{
					"name": gorm.Expr(
						"CASE WHEN COALESCE(people.name, '') = '' THEN excluded.name ELSE people.name END"),
					"tmdb_id": gorm.Expr(
						"CASE WHEN COALESCE(people.tmdb_id, '') = '' THEN excluded.tmdb_id ELSE people.tmdb_id END"),
					"thumb_path": gorm.Expr(
						"CASE WHEN COALESCE(people.thumb_path, '') = '' THEN excluded.thumb_path ELSE people.thumb_path END"),
					"updated_at": now,
				}),
			}).CreateInBatches(people, batchSize).Error; err != nil {
				return err
			}
		}
		return tx.Clauses(clause.OnConflict{DoNothing: true}).
			CreateInBatches(links, batchSize).Error
	})
	if err != nil {
		return 0, err
	}
	return len(links), nil
}
