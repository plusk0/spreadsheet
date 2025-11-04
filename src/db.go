package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	_ "github.com/mattn/go-sqlite3"
)

// Row holds a generic DB row: ID and JSON blob data
type Row struct {
	ID   int
	Data map[string]interface{}
}

// View represents a saved column visibility/view configuration
type View struct {
	ID      int
	Name    string
	Columns []string
}

// initializeDB creates/opens the sqlite database and ensures required tables exist.
// It will also attempt to migrate older schemas into the JSON `data` column.
func initializeDB() *sql.DB {
	// open DB (fatal on error so callers don't have to handle nil)
	db, err := sql.Open("sqlite3", "./data.db")
	if err != nil {
		// try rwc as a fallback
		db, err = sql.Open("sqlite3", "file:./data.db?mode=rwc")
		if err != nil {
			log.Fatalf("unable to open or create database: %v", err)
		}
	}

	if err := db.Ping(); err != nil {
		db2, err2 := sql.Open("sqlite3", "file:./data.db?mode=rwc")
		if err2 != nil {
			log.Fatalf("failed to connect to DB: %v / %v", err, err2)
		}
		if err := db2.Ping(); err != nil {
			log.Fatalf("failed to ping newly opened DB: %v", err)
		}
		db = db2
	}

	// Ensure entries table exists
	createEntries := `
	CREATE TABLE IF NOT EXISTS entries (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		data TEXT
	);
	`
	if _, err := db.Exec(createEntries); err != nil {
		log.Fatalf("failed ensuring entries table exists: %v", err)
	}

	// Ensure views table exists (stores name + JSON array of column names)
	createViews := `
	CREATE TABLE IF NOT EXISTS views (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		name TEXT,
		data TEXT
	);
	`
	if _, err := db.Exec(createViews); err != nil {
		log.Fatalf("failed ensuring views table exists: %v", err)
	}

	// Migration block: if entries exists but doesn't have data column migrate older layout
	cols := []string{}
	rows, err := db.Query("PRAGMA table_info(entries)")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var cid int
			var name, ctype string
			var notnull, pk int
			var dflt sql.NullString
			if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
				log.Printf("warning: scanning pragma result: %v", err)
				continue
			}
			cols = append(cols, name)
		}
		if err := rows.Err(); err != nil {
			log.Printf("warning: pragma rows err: %v", err)
		}
	} else {
		log.Printf("warning: unable to read table info: %v", err)
	}

	hasData := false
	for _, c := range cols {
		if c == "data" {
			hasData = true
			break
		}
	}

	// migrate if needed (existing table but no 'data' column)
	if !hasData && len(cols) > 0 {
		oldCols := cols
		selectSQL := "SELECT "
		for i, c := range oldCols {
			if i > 0 {
				selectSQL += ", "
			}
			selectSQL += fmt.Sprintf("%q", c)
		}
		selectSQL += " FROM entries ORDER BY id"

		orow, err := db.Query(selectSQL)
		if err != nil {
			log.Printf("migration: failed to query old entries table: %v", err)
		} else {
			defer orow.Close()

			_, err = db.Exec(`CREATE TABLE IF NOT EXISTS entries_new (id INTEGER PRIMARY KEY AUTOINCREMENT, data TEXT)`)
			if err != nil {
				log.Fatalf("migration: failed to create entries_new: %v", err)
			}

			colsList, _ := orow.Columns()
			for orow.Next() {
				scanArgs := make([]interface{}, len(colsList))
				for i := range scanArgs {
					var v interface{}
					scanArgs[i] = &v
				}
				if err := orow.Scan(scanArgs...); err != nil {
					log.Printf("migration: failed scanning row: %v", err)
					continue
				}

				var idInt int64 = 0
				m := map[string]interface{}{}
				for i, col := range colsList {
					valPtr := scanArgs[i].(*interface{})
					val := *valPtr
					if col == "id" {
						switch t := val.(type) {
						case int64:
							idInt = t
						case int:
							idInt = int64(t)
						case nil:
							idInt = 0
						default:
							fmt.Sscanf(fmt.Sprintf("%v", t), "%d", &idInt)
						}
						continue
					}
					if val == nil {
						m[col] = nil
						continue
					}
					switch v := val.(type) {
					case int64:
						m[col] = int(v)
					case float64:
						m[col] = v
					case []byte:
						m[col] = string(v)
					case string:
						m[col] = v
					default:
						m[col] = fmt.Sprintf("%v", v)
					}
				}

				js, err := json.Marshal(m)
				if err != nil {
					log.Printf("migration: json marshal error for id=%d: %v", idInt, err)
					continue
				}
				if idInt > 0 {
					if _, err := db.Exec("INSERT INTO entries_new(id, data) VALUES(?, ?)", idInt, string(js)); err != nil {
						log.Printf("migration: insert into entries_new failed for id=%d: %v", idInt, err)
						continue
					}
				} else {
					if _, err := db.Exec("INSERT INTO entries_new(data) VALUES(?)", string(js)); err != nil {
						log.Printf("migration: insert into entries_new failed: %v", err)
						continue
					}
				}
			}
			if err := orow.Err(); err != nil {
				log.Printf("migration: rows error: %v", err)
			}

			if _, err := db.Exec("DROP TABLE entries"); err != nil {
				log.Fatalf("migration: failed to drop old entries table: %v", err)
			}
			if _, err := db.Exec("ALTER TABLE entries_new RENAME TO entries"); err != nil {
				log.Fatalf("migration: failed to rename entries_new: %v", err)
			}

			var maxID sql.NullInt64
			if err := db.QueryRow("SELECT MAX(id) FROM entries").Scan(&maxID); err == nil && maxID.Valid {
				_, _ = db.Exec("INSERT OR REPLACE INTO sqlite_sequence(name, seq) VALUES(?, ?)", "entries", maxID.Int64)
			}
		}
	}

	return db
}

// insertRow inserts a row json blob and returns inserted id
func insertRow(db *sql.DB, data map[string]interface{}) (int64, error) {
	js, err := json.Marshal(data)
	if err != nil {
		return 0, err
	}
	res, err := db.Exec("INSERT INTO entries (data) VALUES (?)", string(js))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// getAllRows returns all rows with JSON data parsed into map[string]interface{}
func getAllRows(db *sql.DB) ([]Row, error) {
	rows, err := db.Query("SELECT id, data FROM entries ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Row
	for rows.Next() {
		var id int
		var dataStr string
		if err := rows.Scan(&id, &dataStr); err != nil {
			return nil, err
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(dataStr), &m); err != nil {
			// if invalid JSON, represent as empty map but keep raw string under "_raw"
			m = map[string]interface{}{"_raw": dataStr}
		}
		out = append(out, Row{ID: id, Data: m})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// updateField loads JSON blob, updates the given field and writes it back
func updateField(db *sql.DB, id int, field string, value interface{}) error {
	// load existing
	var dataStr string
	if err := db.QueryRow("SELECT data FROM entries WHERE id = ?", id).Scan(&dataStr); err != nil {
		return err
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(dataStr), &m); err != nil {
		m = map[string]interface{}{}
	}
	m[field] = value
	js, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = db.Exec("UPDATE entries SET data = ? WHERE id = ?", string(js), id)
	return err
}

// deleteRow deletes a row by id
func deleteRow(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM entries WHERE id = ?", id)
	return err
}

// replaceRow replaces the entire data map for a row
func replaceRow(db *sql.DB, id int, data map[string]interface{}) error {
	js, err := json.Marshal(data)
	if err != nil {
		return err
	}
	_, err = db.Exec("UPDATE entries SET data = ? WHERE id = ?", string(js), id)
	return err
}

// getEmptyRowFromSchema returns a map with default values based on field types
func getEmptyRowFromSchema(schema []FieldDef) map[string]interface{} {
	m := map[string]interface{}{}
	for _, f := range schema {
		switch f.Type {
		case "int":
			m[f.Name] = 0
		case "string":
			m[f.Name] = ""
		case "[]string":
			m[f.Name] = []string{}
		default:
			m[f.Name] = ""
		}
	}
	return m
}

// attachIDToDataMap returns a copy of data with ID field included
func attachIDToDataMap(id int, data map[string]interface{}) map[string]interface{} {
	newMap := map[string]interface{}{}
	for k, v := range data {
		newMap[k] = v
	}
	newMap["ID"] = id
	return newMap
}

// dumpRow is debug helper
func dumpRow(r Row) string {
	b, _ := json.MarshalIndent(r.Data, "", "  ")
	return fmt.Sprintf("id=%d data=%s", r.ID, string(b))
}

// --- Views management --- //

// getAllViews returns all stored views (does not include implicit "All" view)
func getAllViews(db *sql.DB) ([]View, error) {
	rows, err := db.Query("SELECT id, name, data FROM views ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []View
	for rows.Next() {
		var id int
		var name string
		var dataStr string
		if err := rows.Scan(&id, &name, &dataStr); err != nil {
			return nil, err
		}
		var cols []string
		if err := json.Unmarshal([]byte(dataStr), &cols); err != nil {
			cols = []string{}
		}
		out = append(out, View{ID: id, Name: name, Columns: cols})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// insertView creates a new view entry and returns its id
func insertView(db *sql.DB, name string, cols []string) (int64, error) {
	js, err := json.Marshal(cols)
	if err != nil {
		return 0, err
	}
	res, err := db.Exec("INSERT INTO views (name, data) VALUES (?, ?)", name, string(js))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// updateView updates an existing view
func updateView(db *sql.DB, id int, name string, cols []string) error {
	js, err := json.Marshal(cols)
	if err != nil {
		return err
	}
	_, err = db.Exec("UPDATE views SET name = ?, data = ? WHERE id = ?", name, string(js), id)
	return err
}

// deleteView deletes a single view by id
func deleteView(db *sql.DB, id int) error {
	_, err := db.Exec("DELETE FROM views WHERE id = ?", id)
	return err
}

// deleteAllViews removes all stored views (used when importing)
func deleteAllViews(db *sql.DB) error {
	_, err := db.Exec("DELETE FROM views")
	return err
}
