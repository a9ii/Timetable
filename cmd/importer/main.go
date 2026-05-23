// =============================================================================
// FILE: cmd/importer/main.go
//
// PURPOSE:
//   Standalone CLI tool that reads Data.json (aSc Timetables export format),
//   maps it to the system's GORM models, and directly inserts all records into
//   the PostgreSQL database in the correct dependency order:
//
//   DayDefs → Periods → Subjects → Teachers → Classrooms → Classes → Lessons
//
//   Runs as a one-shot process (not a web server).  Safe to run multiple times —
//   it clears existing master data and re-imports cleanly each time.
//
// USAGE:
//   go run ./cmd/importer/main.go
//   (must be run from the TimeTable/ project root with a valid .env file)
// =============================================================================
package main

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// =============================================================================
// ASC JSON STRUCTURES — mirrors the aSc Timetables export format
// =============================================================================

type AscRoot struct {
	R AscR `json:"r"`
}

type AscR struct {
	DbiAccessorRes AscDbi `json:"dbiAccessorRes"`
}

type AscDbi struct {
	Tables []AscTable `json:"tables"`
}

type AscTable struct {
	ID       string          `json:"id"`
	DataRows json.RawMessage `json:"data_rows"`
}

// aSc Period row
type AscPeriod struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	StartTime string `json:"starttime"`
	EndTime   string `json:"endtime"`
}

// aSc DaysDef row — binary flags per day
type AscDaysDef struct {
	ID   string   `json:"id"`
	Vals []string `json:"vals"`
}

// aSc Days row (the actual operating days)
type AscDay struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// aSc Subject row
type AscSubject struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Short string `json:"short"`
	Color string `json:"color"`
}

// aSc Teacher row
type AscTeacher struct {
	ID    string `json:"id"`
	Short string `json:"short"`
	Color string `json:"color"`
}

// aSc Classroom row
type AscClassroom struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Short string `json:"short"`
	Color string `json:"color"`
}

// aSc Class row
type AscClass struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Short string `json:"short"`
	Color string `json:"color"`
}

// aSc Lesson row
type AscLesson struct {
	ID              string   `json:"id"`
	SubjectID       string   `json:"subjectid"`
	TeacherIDs      []string `json:"teacherids"`
	ClassIDs        []string `json:"classids"`
	Count           int      `json:"count"`
	DurationPeriods int      `json:"durationperiods"`
}

// =============================================================================
// GORM MODELS (replicated here so this is a standalone binary)
// =============================================================================

type UUIDArray []uuid.UUID

func (a UUIDArray) GormDataType() string { return "text[]" }

func (a UUIDArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	parts := make([]string, len(a))
	for i, u := range a {
		parts[i] = u.String()
	}
	return "{" + strings.Join(parts, ",") + "}", nil
}

func (a *UUIDArray) Scan(value interface{}) error {
	if value == nil { *a = UUIDArray{}; return nil }
	var str string
	switch v := value.(type) {
	case string:
		str = v
	case []byte:
		str = string(v)
	default:
		return fmt.Errorf("unsupported type %T", value)
	}
	str = strings.TrimPrefix(strings.TrimSuffix(str, "}"), "{")
	if str == "" { *a = UUIDArray{}; return nil }
	parts := strings.Split(str, ",")
	res := make(UUIDArray, 0, len(parts))
	for _, p := range parts {
		u, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil { return err }
		res = append(res, u)
	}
	*a = res
	return nil
}

type Teacher struct {
	ID                uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name              string    `gorm:"not null;uniqueIndex"`
	ShortCode         string    `gorm:"size:10"`
	MaxPeriodsPerWeek int
	Color             string `gorm:"size:10"`
}

type Class struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name     string    `gorm:"not null;uniqueIndex"`
	Capacity int
	Shift    string    `gorm:"size:20"`
}

type Subject struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name        string    `gorm:"not null;uniqueIndex"`
	RequiresLab bool
}

type Classroom struct {
	ID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	Name     string    `gorm:"not null;uniqueIndex"`
	IsLab    bool
	Capacity int
}

type Period struct {
	ID        string `gorm:"primaryKey;size:20"`
	Name      string `gorm:"not null"`
	StartTime string `gorm:"size:8"`
	EndTime   string `gorm:"size:8"`
	Shift     string `gorm:"size:20"`
	Index     int
}

type DayDef struct {
	ID          string `gorm:"primaryKey;size:20"`
	Name        string `gorm:"not null"`
	BinaryValue string `gorm:"size:10"`
	Index       int
}

type Lesson struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	SubjectID    uuid.UUID `gorm:"type:uuid;not null"`
	TeacherIDs   UUIDArray `gorm:"type:text[]"`
	ClassIDs     UUIDArray `gorm:"type:text[]"`
	CountPerWeek int
}

type Card struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey"`
	LessonID    uuid.UUID  `gorm:"type:uuid"`
	PeriodID    string     `gorm:"size:20"`
	DayDefID    string     `gorm:"size:20"`
	ClassroomID *uuid.UUID `gorm:"type:uuid"`
}

// =============================================================================
// HELPERS
// =============================================================================

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" { return v }
	return fallback
}

// tableByID finds a table in the aSc JSON by its id field.
func tableByID(tables []AscTable, id string) *AscTable {
	for i := range tables {
		if tables[i].ID == id {
			return &tables[i]
		}
	}
	return nil
}

// cleanName sanitises a string that may come back with encoding artefacts.
// aSc exports Arabic text in UTF-8; PowerShell showed it garbled but Go will
// read it correctly from the raw bytes.
func cleanName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "—"
	}
	return s
}

// The aSc "days" table only contains ids/names for days defined in daysdefs.
// We use the daysdefs binary values as the canonical list.
var dayNames = map[string]string{
	"0": "Sunday",
	"1": "Monday",
	"2": "Tuesday",
	"3": "Wednesday",
	"4": "Thursday",
	"5": "Friday",
	"6": "Saturday",
}

// =============================================================================
// MAIN
// =============================================================================

func main() {
	// Load .env
	_ = godotenv.Load()

	// Build DSN
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			getEnv("DB_HOST", "localhost"),
			getEnv("DB_PORT", "5432"),
			getEnv("DB_USER", "postgres"),
			getEnv("DB_PASS", "postgres"),
			getEnv("DB_NAME", "timetable"),
		)
	}

	// Connect to DB with silent logger
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		log.Fatalf("DB connect failed: %v", err)
	}
	log.Println("✅ Connected to database.")

	// AutoMigrate
	if err := db.AutoMigrate(&Teacher{}, &Class{}, &Subject{}, &Classroom{}, &Period{}, &DayDef{}, &Lesson{}, &Card{}); err != nil {
		log.Fatalf("AutoMigrate failed: %v", err)
	}

	// Read Data.json
	dataPath := "Data.json"
	raw, err := os.ReadFile(dataPath)
	if err != nil {
		log.Fatalf("Cannot read %s: %v", dataPath, err)
	}

	var root AscRoot
	if err := json.Unmarshal(raw, &root); err != nil {
		log.Fatalf("JSON parse failed: %v", err)
	}
	tables := root.R.DbiAccessorRes.Tables
	log.Printf("✅ Parsed Data.json — %d tables found.", len(tables))

	// =========================================================================
	// WIPE existing data in reverse dependency order
	// =========================================================================
	log.Println("🗑  Clearing existing data...")
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Card{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Lesson{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Class{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Classroom{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Teacher{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Subject{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Period{})
	db.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&DayDef{})
	log.Println("✅ Old data cleared.")

	// =========================================================================
	// 1. PERIODS
	// =========================================================================
	periodTable := tableByID(tables, "periods")
	if periodTable == nil {
		log.Fatal("periods table not found in JSON")
	}
	var ascPeriods []AscPeriod
	if err := json.Unmarshal(periodTable.DataRows, &ascPeriods); err != nil {
		log.Fatalf("periods parse: %v", err)
	}

	periods := make([]Period, 0, len(ascPeriods))
	for i, p := range ascPeriods {
		shift := ""
		if p.Name == "1" || p.Name == "2" || p.Name == "3" {
			shift = "morning"
		} else if p.Name == "4" || p.Name == "5" || p.Name == "6" {
			shift = "evening"
		}

		periods = append(periods, Period{
			ID:        "P" + p.ID,
			Name:      cleanName(p.Name),
			StartTime: p.StartTime,
			EndTime:   p.EndTime,
			Shift:     shift,
			Index:     i,
		})
	}
	if err := db.Create(&periods).Error; err != nil {
		log.Fatalf("Insert periods: %v", err)
	}
	log.Printf("✅ Inserted %d periods.", len(periods))

	// =========================================================================
	// 2. DAYS (from the "days" table — actual school days with names)
	// =========================================================================
	daysTable := tableByID(tables, "days")
	var days []DayDef
	if daysTable != nil {
		var ascDays []AscDay
		_ = json.Unmarshal(daysTable.DataRows, &ascDays)
		for i, d := range ascDays {
			name := cleanName(d.Name)
			if name == "—" {
				// fallback to positional name
				if n, ok := dayNames[fmt.Sprintf("%d", i)]; ok {
					name = n
				} else {
					name = fmt.Sprintf("Day %d", i+1)
				}
			}
			days = append(days, DayDef{
				ID:          "D" + d.ID,
				Name:        name,
				BinaryValue: "",
				Index:       i,
			})
		}
	}

	// If days table is empty/missing, fall back to daysdefs
	if len(days) == 0 {
		daysDefs := tableByID(tables, "daysdefs")
		if daysDefs != nil {
			var defs []AscDaysDef
			_ = json.Unmarshal(daysDefs.DataRows, &defs)
			// Use only single-day defs (binary with one '1')
			idx := 0
			for _, d := range defs {
				if len(d.Vals) == 1 && strings.Count(d.Vals[0], "1") == 1 {
					name := ""
					for pos, ch := range d.Vals[0] {
						if ch == '1' {
							name = dayNames[fmt.Sprintf("%d", pos)]
							break
						}
					}
					if name == "" {
						name = fmt.Sprintf("Day %d", idx+1)
					}
					days = append(days, DayDef{
						ID:          strings.TrimPrefix(d.ID, "*"),
						Name:        name,
						BinaryValue: d.Vals[0],
						Index:       idx,
					})
					idx++
				}
			}
		}
	}

	if len(days) == 0 {
		// Hard fallback: Sunday–Thursday
		defaults := []struct{ id, name, bin string }{
			{"D0", "Sunday", "10000"}, {"D1", "Monday", "01000"},
			{"D2", "Tuesday", "00100"}, {"D3", "Wednesday", "00010"},
			{"D4", "Thursday", "00001"},
		}
		for i, d := range defaults {
			days = append(days, DayDef{ID: d.id, Name: d.name, BinaryValue: d.bin, Index: i})
		}
	}

	if err := db.Create(&days).Error; err != nil {
		log.Fatalf("Insert days: %v", err)
	}
	log.Printf("✅ Inserted %d days.", len(days))

	// =========================================================================
	// 3. SUBJECTS  — build asc-id → UUID map for lessons
	// =========================================================================
	subjectTable := tableByID(tables, "subjects")
	if subjectTable == nil {
		log.Fatal("subjects table not found")
	}
	var ascSubjects []AscSubject
	if err := json.Unmarshal(subjectTable.DataRows, &ascSubjects); err != nil {
		log.Fatalf("subjects parse: %v", err)
	}

	ascSubjectToUUID := make(map[string]uuid.UUID, len(ascSubjects))
	subjects := make([]Subject, 0, len(ascSubjects))
	seen := make(map[string]bool)
	for _, s := range ascSubjects {
		name := cleanName(s.Name)
		if name == "—" {
			name = cleanName(s.Short)
		}
		if name == "—" {
			name = "Subject " + s.ID
		}
		// deduplicate names
		orig := name
		counter := 2
		for seen[name] {
			name = fmt.Sprintf("%s (%d)", orig, counter)
			counter++
		}
		seen[name] = true

		id := uuid.New()
		ascSubjectToUUID[s.ID] = id
		subjects = append(subjects, Subject{
			ID:          id,
			Name:        name,
			RequiresLab: false, // aSc doesn't export this flag; extend later
		})
	}
	if err := db.Create(&subjects).Error; err != nil {
		log.Fatalf("Insert subjects: %v", err)
	}
	log.Printf("✅ Inserted %d subjects.", len(subjects))

	// =========================================================================
	// 4. TEACHERS  — asc-id → UUID map
	// =========================================================================
	teacherTable := tableByID(tables, "teachers")
	if teacherTable == nil {
		log.Fatal("teachers table not found")
	}
	var ascTeachers []AscTeacher
	if err := json.Unmarshal(teacherTable.DataRows, &ascTeachers); err != nil {
		log.Fatalf("teachers parse: %v", err)
	}

	ascTeacherToUUID := make(map[string]uuid.UUID, len(ascTeachers))
	teachers := make([]Teacher, 0, len(ascTeachers))
	seenTeacher := make(map[string]bool)
	for i, t := range ascTeachers {
		name := cleanName(t.Short)
		if name == "—" {
			name = fmt.Sprintf("Teacher %d", i+1)
		}
		orig := name
		counter := 2
		for seenTeacher[name] {
			name = fmt.Sprintf("%s (%d)", orig, counter)
			counter++
		}
		seenTeacher[name] = true

		color := t.Color
		if color == "" {
			color = "#6366f1"
		}
		// ShortCode column is varchar(10) — safe truncation
		shortCode := name
		if len([]rune(shortCode)) > 10 {
			runes := []rune(shortCode)
			shortCode = string(runes[:10])
		}
		id := uuid.New()
		ascTeacherToUUID[t.ID] = id
		teachers = append(teachers, Teacher{
			ID:                id,
			Name:              name,
			ShortCode:         shortCode,
			MaxPeriodsPerWeek: 30,
			Color:             color,
		})
	}
	if err := db.Create(&teachers).Error; err != nil {
		log.Fatalf("Insert teachers: %v", err)
	}
	log.Printf("✅ Inserted %d teachers.", len(teachers))

	// =========================================================================
	// 5. CLASSROOMS
	// =========================================================================
	classroomTable := tableByID(tables, "classrooms")
	if classroomTable == nil {
		log.Fatal("classrooms table not found")
	}
	var ascClassrooms []AscClassroom
	if err := json.Unmarshal(classroomTable.DataRows, &ascClassrooms); err != nil {
		log.Fatalf("classrooms parse: %v", err)
	}

	classrooms := make([]Classroom, 0, len(ascClassrooms))
	seenRoom := make(map[string]bool)
	for i, r := range ascClassrooms {
		name := cleanName(r.Name)
		if name == "—" {
			name = cleanName(r.Short)
		}
		if name == "—" {
			name = fmt.Sprintf("Room %d", i+1)
		}
		orig := name
		counter := 2
		for seenRoom[name] {
			name = fmt.Sprintf("%s (%d)", orig, counter)
			counter++
		}
		seenRoom[name] = true

		classrooms = append(classrooms, Classroom{
			ID:       uuid.New(),
			Name:     name,
			IsLab:    false,
			Capacity: 35,
		})
	}
	if err := db.Create(&classrooms).Error; err != nil {
		log.Fatalf("Insert classrooms: %v", err)
	}
	log.Printf("✅ Inserted %d classrooms.", len(classrooms))

	// =========================================================================
	// 6. CLASSES  — asc-id → UUID map
	// =========================================================================
	classTable := tableByID(tables, "classes")
	if classTable == nil {
		log.Fatal("classes table not found")
	}
	var ascClasses []AscClass
	if err := json.Unmarshal(classTable.DataRows, &ascClasses); err != nil {
		log.Fatalf("classes parse: %v", err)
	}

	ascClassToUUID := make(map[string]uuid.UUID, len(ascClasses))
	classes := make([]Class, 0, len(ascClasses))
	seenClass := make(map[string]bool)
	for i, c := range ascClasses {
		name := cleanName(c.Name)
		if name == "—" {
			name = cleanName(c.Short)
		}
		if name == "—" {
			name = fmt.Sprintf("Class %d", i+1)
		}
		orig := name
		counter := 2
		for seenClass[name] {
			name = fmt.Sprintf("%s (%d)", orig, counter)
			counter++
		}
		seenClass[name] = true

		shift := ""
		if strings.Contains(name, "صباحي") {
			shift = "morning"
		} else if strings.Contains(name, "مسائي") {
			shift = "evening"
		}

		id := uuid.New()
		ascClassToUUID[c.ID] = id
		classes = append(classes, Class{
			ID:       id,
			Name:     name,
			Capacity: 30,
			Shift:    shift,
		})
	}
	if err := db.Create(&classes).Error; err != nil {
		log.Fatalf("Insert classes: %v", err)
	}
	log.Printf("✅ Inserted %d classes.", len(classes))

	// =========================================================================
	// 7. LESSONS
	// =========================================================================
	lessonTable := tableByID(tables, "lessons")
	if lessonTable == nil {
		log.Fatal("lessons table not found")
	}
	var ascLessons []AscLesson
	if err := json.Unmarshal(lessonTable.DataRows, &ascLessons); err != nil {
		log.Fatalf("lessons parse: %v", err)
	}

	lessonMap := make(map[string]*Lesson)
	skipped := 0
	for _, l := range ascLessons {
		// Resolve subjectID
		subjectUID, ok := ascSubjectToUUID[l.SubjectID]
		if !ok {
			skipped++
			continue // skip lessons with unknown subject
		}

		// Resolve teacherIDs
		teacherUIDs := make(UUIDArray, 0, len(l.TeacherIDs))
		for _, tid := range l.TeacherIDs {
			if uid, ok := ascTeacherToUUID[tid]; ok {
				teacherUIDs = append(teacherUIDs, uid)
			}
		}

		// Resolve classIDs
		classUIDs := make(UUIDArray, 0, len(l.ClassIDs))
		for _, cid := range l.ClassIDs {
			if uid, ok := ascClassToUUID[cid]; ok {
				classUIDs = append(classUIDs, uid)
			}
		}
		if len(classUIDs) == 0 {
			skipped++
			continue // can't schedule without a class
		}

		count := l.Count
		if count <= 0 {
			count = 1
		}
		duration := l.DurationPeriods
		if duration <= 0 {
			duration = 1
		}
		
		totalPeriods := count * duration

		// Create a unique key for deduplication
		tStrs := make([]string, len(teacherUIDs))
		for i, tid := range teacherUIDs { tStrs[i] = tid.String() }
		cStrs := make([]string, len(classUIDs))
		for i, cid := range classUIDs { cStrs[i] = cid.String() }
		
		key := fmt.Sprintf("%s|%s|%s", subjectUID.String(), strings.Join(tStrs, ","), strings.Join(cStrs, ","))

		if existing, ok := lessonMap[key]; ok {
			existing.CountPerWeek += totalPeriods
		} else {
			lessonMap[key] = &Lesson{
				ID:           uuid.New(),
				SubjectID:    subjectUID,
				TeacherIDs:   teacherUIDs,
				ClassIDs:     classUIDs,
				CountPerWeek: totalPeriods,
			}
		}
	}

	lessons := make([]Lesson, 0, len(lessonMap))
	for _, l := range lessonMap {
		lessons = append(lessons, *l)
	}

	// Batch insert lessons in chunks of 50
	const batchSize = 50
	totalInserted := 0
	for i := 0; i < len(lessons); i += batchSize {
		end := i + batchSize
		if end > len(lessons) {
			end = len(lessons)
		}
		if err := db.Create(lessons[i:end]).Error; err != nil {
			log.Fatalf("Insert lessons batch %d: %v", i/batchSize, err)
		}
		totalInserted += end - i
	}
	log.Printf("✅ Inserted %d lessons. (%d skipped due to missing references)", totalInserted, skipped)

	// =========================================================================
	// SUMMARY
	// =========================================================================
	fmt.Println()
	fmt.Println("╔══════════════════════════════════════════╗")
	fmt.Println("║   Import Complete — Summary              ║")
	fmt.Println("╠══════════════════════════════════════════╣")
	fmt.Printf("║  Periods    : %-5d                       ║\n", len(periods))
	fmt.Printf("║  Days       : %-5d                       ║\n", len(days))
	fmt.Printf("║  Subjects   : %-5d                       ║\n", len(subjects))
	fmt.Printf("║  Teachers   : %-5d                       ║\n", len(teachers))
	fmt.Printf("║  Classrooms : %-5d                       ║\n", len(classrooms))
	fmt.Printf("║  Classes    : %-5d                       ║\n", len(classes))
	fmt.Printf("║  Lessons    : %-5d (%-3d skipped)         ║\n", totalInserted, skipped)
	fmt.Println("╚══════════════════════════════════════════╝")
	fmt.Println()
	fmt.Println("The system is ready. Start the server and click 'Generate Timetable'.")
}
