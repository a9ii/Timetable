// =============================================================================
// FILE: models.go
// PACKAGE: main
//
// PURPOSE:
//   Defines all GORM data models (the canonical data schema), database
//   connection bootstrapping, and GORM-level hooks / type overrides needed to
//   persist complex Go types (e.g. []uuid.UUID) into a PostgreSQL database.
//
// DESIGN DECISIONS:
//   - Every primary-key that represents a "domain object" (Teacher, Class, etc.)
//     uses UUID v4 for global uniqueness and safe distribution.
//   - Period and DayDef use string IDs intentionally to let the frontend and
//     configuration files use human-readable keys (e.g. "MON", "P1").
//   - UUIDArray is a custom GORM/PostgreSQL type that serialises a []uuid.UUID
//     as a PostgreSQL TEXT[] column, keeping joins simple and indexes fast.
//   - Card.ClassroomID is a *uuid.UUID (pointer) so the DB column is nullable;
//     not every lesson requires a dedicated room assignment.
// =============================================================================
package main

import (
	"database/sql/driver"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// =============================================================================
// CUSTOM POSTGRESQL TYPES
// =============================================================================

// UUIDArray is a custom type that stores a Go []uuid.UUID as a PostgreSQL
// TEXT[] array column.  GORM's standard driver does not know how to scan
// or value a slice of UUIDs, so we implement the driver.Valuer and
// sql.Scanner interfaces manually.
type UUIDArray []uuid.UUID

// Value converts the Go slice into a PostgreSQL-compatible TEXT[] literal so
// GORM can write it to the database.
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

// Scan reads the raw PostgreSQL TEXT[] value back into the Go []uuid.UUID slice
// so GORM can populate the struct field after a SELECT query.
func (a *UUIDArray) Scan(value interface{}) error {
	if value == nil {
		*a = UUIDArray{}
		return nil
	}
	str, ok := value.(string)
	if !ok {
		// Some drivers return []byte instead of string.
		b, ok2 := value.([]byte)
		if !ok2 {
			return fmt.Errorf("UUIDArray.Scan: unsupported source type %T", value)
		}
		str = string(b)
	}
	// Strip the surrounding braces that PostgreSQL adds.
	str = strings.TrimPrefix(str, "{")
	str = strings.TrimSuffix(str, "}")
	if str == "" {
		*a = UUIDArray{}
		return nil
	}
	parts := strings.Split(str, ",")
	result := make(UUIDArray, 0, len(parts))
	for _, p := range parts {
		u, err := uuid.Parse(strings.TrimSpace(p))
		if err != nil {
			return fmt.Errorf("UUIDArray.Scan: cannot parse UUID %q: %w", p, err)
		}
		result = append(result, u)
	}
	*a = result
	return nil
}

// StringArray is a custom type that stores a Go []string as a PostgreSQL TEXT[] array column.
type StringArray []string

// Value converts the Go slice into a PostgreSQL-compatible TEXT[] literal.
func (a StringArray) Value() (driver.Value, error) {
	if len(a) == 0 {
		return "{}", nil
	}
	// We assume strings do not contain commas or braces for simplicity in this context.
	// For production with arbitrary strings, proper quoting is needed.
	return "{" + strings.Join(a, ",") + "}", nil
}

// Scan reads the raw PostgreSQL TEXT[] value back into the Go []string slice.
func (a *StringArray) Scan(value interface{}) error {
	if value == nil {
		*a = StringArray{}
		return nil
	}
	str, ok := value.(string)
	if !ok {
		b, ok2 := value.([]byte)
		if !ok2 {
			return fmt.Errorf("StringArray.Scan: unsupported source type %T", value)
		}
		str = string(b)
	}
	str = strings.TrimPrefix(str, "{")
	str = strings.TrimSuffix(str, "}")
	if str == "" {
		*a = StringArray{}
		return nil
	}
	parts := strings.Split(str, ",")
	result := make(StringArray, 0, len(parts))
	for _, p := range parts {
		result = append(result, strings.TrimSpace(p))
	}
	*a = result
	return nil
}

// =============================================================================
// MASTER DATA MODELS — The Building Blocks
// =============================================================================

// Teacher represents a teaching staff member.
// MaxPeriodsPerWeek is used by the solver as a soft / hard upper-bound
// heuristic when distributing workload.
// Color is a hex string (e.g. "#3B82F6") used by the frontend to paint cards.
type Teacher struct {
	ID                uuid.UUID   `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name              string      `gorm:"not null;uniqueIndex"                           json:"name"`
	ShortCode         string      `gorm:"not null;size:10"                               json:"shortCode"`
	MaxPeriodsPerWeek int         `gorm:"not null;default:30"                            json:"maxPeriodsPerWeek"`
	Color             string      `gorm:"size:10"                                        json:"color"`
	AvailableDayIDs   StringArray `gorm:"type:text[]"                                    json:"availableDayIds"`
	CreatedAt         time.Time   `                                                      json:"createdAt"`
	UpdatedAt         time.Time   `                                                      json:"updatedAt"`
}

// BeforeCreate hook: always generate a fresh UUID before inserting a new Teacher
// so the client does not need to supply one.
func (t *Teacher) BeforeCreate(tx *gorm.DB) error {
	if t.ID == uuid.Nil {
		t.ID = uuid.New()
	}
	return nil
}

// -------------------------------------------------------------------------

// Class represents a student group/section (e.g. "Grade 10 - Section A").
// Capacity is the maximum number of students; the solver uses this when
// matching classrooms.
type Class struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name      string    `gorm:"not null;uniqueIndex"                           json:"name"`
	Capacity  int       `gorm:"not null;default:30"                            json:"capacity"`
	Shift     string    `gorm:"size:20"                                        json:"shift"`
	CreatedAt time.Time `                                                      json:"createdAt"`
	UpdatedAt time.Time `                                                      json:"updatedAt"`
}

func (c *Class) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// -------------------------------------------------------------------------

// Subject is an academic discipline (e.g. "Mathematics", "Chemistry Lab").
// RequiresLab is critical for the solver: lessons tied to this subject will
// only be placed in Classrooms where IsLab == true.
type Subject struct {
	ID          uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name        string    `gorm:"not null;uniqueIndex"                           json:"name"`
	RequiresLab bool      `gorm:"not null;default:false"                         json:"requiresLab"`
	CreatedAt   time.Time `                                                      json:"createdAt"`
	UpdatedAt   time.Time `                                                      json:"updatedAt"`
}

func (s *Subject) BeforeCreate(tx *gorm.DB) error {
	if s.ID == uuid.Nil {
		s.ID = uuid.New()
	}
	return nil
}

// -------------------------------------------------------------------------

// Classroom is a physical teaching space.
// IsLab=true means only Subject.RequiresLab lessons may be placed here
// (enforced by the solver's constraint engine).
type Classroom struct {
	ID        uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	Name      string    `gorm:"not null;uniqueIndex"                           json:"name"`
	IsLab     bool      `gorm:"not null;default:false"                         json:"isLab"`
	Capacity  int       `gorm:"not null;default:30"                            json:"capacity"`
	CreatedAt time.Time `                                                      json:"createdAt"`
	UpdatedAt time.Time `                                                      json:"updatedAt"`
}

func (r *Classroom) BeforeCreate(tx *gorm.DB) error {
	if r.ID == uuid.Nil {
		r.ID = uuid.New()
	}
	return nil
}

// -------------------------------------------------------------------------

// Period represents a named time-slot in the school day (e.g. "P1 08:00-08:45").
// ID is a human-readable string key (e.g. "P1", "P2") so the frontend can
// reference periods without UUID lookups.
// Index is the sort order used to arrange columns in the timetable grid.
type Period struct {
	ID        string    `gorm:"primaryKey;size:20" json:"id"`
	Name      string    `gorm:"not null"           json:"name"`
	StartTime string    `gorm:"size:8"             json:"startTime"` // "HH:MM"
	EndTime   string    `gorm:"size:8"             json:"endTime"`   // "HH:MM"
	Index     int       `gorm:"not null;default:0" json:"index"`
	Shift     string    `gorm:"size:20"            json:"shift"`
	CreatedAt time.Time `                          json:"createdAt"`
	UpdatedAt time.Time `                          json:"updatedAt"`
}

// -------------------------------------------------------------------------

// DayDef represents an operating school day.
// BinaryValue is an aSc-style bitmap string (e.g. "10000" = Sunday only) used
// for future rotation/cycle scheduling extensions.
// Index governs the row order in the timetable grid.
type DayDef struct {
	ID          string    `gorm:"primaryKey;size:20" json:"id"`
	Name        string    `gorm:"not null"           json:"name"`
	BinaryValue string    `gorm:"size:10"            json:"binaryValue"`
	Index       int       `gorm:"not null;default:0" json:"index"`
	CreatedAt   time.Time `                          json:"createdAt"`
	UpdatedAt   time.Time `                          json:"updatedAt"`
}

// =============================================================================
// DOMAIN LOGIC MODELS
// =============================================================================

// Lesson represents a TEACHING REQUIREMENT — not a physical slot.
// It answers the question: "Who teaches What to Whom, and how many times?"
//
//   - TeacherIDs: one or more teachers co-teaching (e.g. two instructors for lab).
//   - ClassIDs:   one or more classes combined (e.g. merged Grade 11A + 11B).
//   - CountPerWeek: the solver will generate exactly this many Card records.
//
// The solver "flattens" each Lesson into CountPerWeek individual tasks before
// running the backtracking search.
type Lesson struct {
	ID           uuid.UUID `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	SubjectID    uuid.UUID `gorm:"type:uuid;not null;index"                       json:"subjectId"`
	Subject      Subject   `gorm:"foreignKey:SubjectID"                           json:"subject,omitempty"`
	TeacherIDs   UUIDArray `gorm:"type:text[]"                                    json:"teacherIds"`
	ClassIDs     UUIDArray `gorm:"type:text[]"                                    json:"classIds"`
	CountPerWeek int       `gorm:"not null;default:1"                             json:"countPerWeek"`
	CreatedAt    time.Time `                                                      json:"createdAt"`
	UpdatedAt    time.Time `                                                      json:"updatedAt"`
}

func (l *Lesson) BeforeCreate(tx *gorm.DB) error {
	if l.ID == uuid.Nil {
		l.ID = uuid.New()
	}
	return nil
}

// -------------------------------------------------------------------------

// Card represents a PHYSICALLY SCHEDULED SLOT — the solver's output.
// Each Card binds a Lesson to a specific Day + Period (and optionally a Room).
//
//   - LessonID:   links back to the requirement that generated this card.
//   - PeriodID:   which time-slot column on the grid.
//   - DayDefID:   which row (day) on the grid.
//   - ClassroomID: nullable — the room assigned by the solver (if rooms exist).
//
// The solver guarantees that no two Cards share the same
// (Teacher, Day, Period), (Class, Day, Period), or (Classroom, Day, Period).
type Card struct {
	ID          uuid.UUID  `gorm:"type:uuid;primaryKey;default:gen_random_uuid()" json:"id"`
	LessonID    uuid.UUID  `gorm:"type:uuid;not null;index"                       json:"lessonId"`
	Lesson      Lesson     `gorm:"foreignKey:LessonID"                            json:"lesson,omitempty"`
	PeriodID    string     `gorm:"size:20;not null;index"                         json:"periodId"`
	DayDefID    string     `gorm:"size:20;not null;index"                         json:"dayDefId"`
	ClassroomID *uuid.UUID `gorm:"type:uuid"                                      json:"classroomId"`
	CreatedAt   time.Time  `                                                      json:"createdAt"`
	UpdatedAt   time.Time  `                                                      json:"updatedAt"`
}

func (c *Card) BeforeCreate(tx *gorm.DB) error {
	if c.ID == uuid.Nil {
		c.ID = uuid.New()
	}
	return nil
}

// =============================================================================
// DATABASE INITIALISATION
// =============================================================================

// InitDB opens the PostgreSQL connection using the DATABASE_URL environment
// variable, configures the connection pool, and auto-migrates all models.
//
// The function returns a ready-to-use *gorm.DB or panics on failure so that
// mis-configuration surfaces immediately at startup rather than silently later.
func InitDB() *gorm.DB {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		// Fallback to individual env vars for local development convenience.
		host := getEnv("DB_HOST", "localhost")
		port := getEnv("DB_PORT", "5432")
		user := getEnv("DB_USER", "postgres")
		pass := getEnv("DB_PASS", "postgres")
		name := getEnv("DB_NAME", "timetable")
		dsn = fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=UTC",
			host, port, user, pass, name,
		)
	}

	// Configure a structured GORM logger that writes to stdout.
	gormLogger := logger.New(
		log.New(os.Stdout, "\r\n", log.LstdFlags),
		logger.Config{
			SlowThreshold:             200 * time.Millisecond,
			LogLevel:                  logger.Warn,
			IgnoreRecordNotFoundError: true,
			Colorful:                  true,
		},
	)

	// PreferSimpleProtocol=true disables pgx named prepared statements.
	// This is REQUIRED for Neon / pgBouncer (transaction pooling mode) which
	// returns SQLSTATE 08P01 "prepared statement name is already in use"
	// when the standard extended query protocol is used.
	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{
		Logger: gormLogger,
	})
	if err != nil {
		log.Fatalf("[DB] Failed to connect to PostgreSQL: %v", err)
	}

	// Configure the underlying sql.DB connection pool.
	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("[DB] Failed to get underlying sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)

	// AutoMigrate creates or alters tables to match struct definitions.
	// It never drops columns, making it safe to run on every startup.
	err = db.AutoMigrate(
		&Teacher{},
		&Class{},
		&Subject{},
		&Classroom{},
		&Period{},
		&DayDef{},
		&Lesson{},
		&Card{},
	)
	if err != nil {
		log.Fatalf("[DB] AutoMigrate failed: %v", err)
	}

	log.Println("[DB] Connection established and schema migrated successfully.")
	return db
}

// SeedDemoData populates the database with a realistic school schedule dataset
// if the tables are empty.  This ensures the system is immediately usable
// after a fresh installation without any manual data entry.
func SeedDemoData(db *gorm.DB) {
	// Only seed if Teachers table is empty.
	var count int64
	db.Model(&Teacher{}).Count(&count)
	if count > 0 {
		log.Println("[Seed] Database already contains data — skipping seed.")
		return
	}
	log.Println("[Seed] Seeding demo data...")

	// --- Days ---
	days := []DayDef{
		{ID: "SUN", Name: "Sunday", BinaryValue: "10000", Index: 0},
		{ID: "MON", Name: "Monday", BinaryValue: "01000", Index: 1},
		{ID: "TUE", Name: "Tuesday", BinaryValue: "00100", Index: 2},
		{ID: "WED", Name: "Wednesday", BinaryValue: "00010", Index: 3},
		{ID: "THU", Name: "Thursday", BinaryValue: "00001", Index: 4},
	}
	db.Create(&days)

	// --- Periods ---
	periods := []Period{
		{ID: "P1", Name: "1st", StartTime: "07:30", EndTime: "08:15", Index: 0, Shift: "morning"},
		{ID: "P2", Name: "2nd", StartTime: "08:15", EndTime: "09:00", Index: 1, Shift: "morning"},
		{ID: "P3", Name: "3rd", StartTime: "09:15", EndTime: "10:00", Index: 2, Shift: "morning"},
		{ID: "P4", Name: "4th", StartTime: "10:00", EndTime: "10:45", Index: 3, Shift: "evening"},
		{ID: "P5", Name: "5th", StartTime: "11:00", EndTime: "11:45", Index: 4, Shift: "evening"},
		{ID: "P6", Name: "6th", StartTime: "11:45", EndTime: "12:30", Index: 5, Shift: "evening"},
	}
	db.Create(&periods)

	// --- Teachers ---
	teachers := []Teacher{
		{Name: "Dr. Ahmed Hassan", ShortCode: "AH", MaxPeriodsPerWeek: 20, Color: "#6366f1", AvailableDayIDs: StringArray{"SUN", "MON", "TUE"}},
		{Name: "Ms. Sara El-Sayed", ShortCode: "SE", MaxPeriodsPerWeek: 18, Color: "#ec4899"},
		{Name: "Mr. Karim Nour", ShortCode: "KN", MaxPeriodsPerWeek: 22, Color: "#f59e0b", AvailableDayIDs: StringArray{"WED", "THU"}},
		{Name: "Dr. Layla Mostafa", ShortCode: "LM", MaxPeriodsPerWeek: 16, Color: "#10b981"},
		{Name: "Mr. Omar Farid", ShortCode: "OF", MaxPeriodsPerWeek: 20, Color: "#3b82f6"},
	}
	for i := range teachers {
		teachers[i].ID = uuid.New()
	}
	db.Create(&teachers)

	// --- Classes ---
	classes := []Class{
		{Name: "Grade 10 - A (صباحي)", Capacity: 28, Shift: "morning"},
		{Name: "Grade 10 - B (صباحي)", Capacity: 30, Shift: "morning"},
		{Name: "Grade 11 - Science (مسائي)", Capacity: 25, Shift: "evening"},
		{Name: "Grade 11 - Arts (مسائي)", Capacity: 32, Shift: "evening"},
	}
	for i := range classes {
		classes[i].ID = uuid.New()
	}
	db.Create(&classes)

	// --- Subjects ---
	subjects := []Subject{
		{Name: "Mathematics", RequiresLab: false},
		{Name: "Physics", RequiresLab: false},
		{Name: "Chemistry Lab", RequiresLab: true},
		{Name: "Arabic Language", RequiresLab: false},
		{Name: "English Language", RequiresLab: false},
		{Name: "Biology Lab", RequiresLab: true},
	}
	for i := range subjects {
		subjects[i].ID = uuid.New()
	}
	db.Create(&subjects)

	// --- Classrooms ---
	classrooms := []Classroom{
		{Name: "Room 101", IsLab: false, Capacity: 35},
		{Name: "Room 102", IsLab: false, Capacity: 35},
		{Name: "Room 103", IsLab: false, Capacity: 35},
		{Name: "Science Lab A", IsLab: true, Capacity: 20},
		{Name: "Science Lab B", IsLab: true, Capacity: 20},
	}
	for i := range classrooms {
		classrooms[i].ID = uuid.New()
	}
	db.Create(&classrooms)

	// --- Helper maps for subject/teacher/class lookup ---
	subjectMap := make(map[string]uuid.UUID)
	for _, s := range subjects {
		subjectMap[s.Name] = s.ID
	}
	teacherMap := make(map[string]uuid.UUID)
	for _, t := range teachers {
		teacherMap[t.ShortCode] = t.ID
	}
	classMap := make(map[string]uuid.UUID)
	for _, c := range classes {
		classMap[c.Name] = c.ID
	}

	// --- Lessons (teaching requirements) ---
	lessons := []Lesson{
		// Mathematics for Grade 10-A: Dr. Ahmed Hassan, 4 periods/week
		{
			SubjectID:    subjectMap["Mathematics"],
			TeacherIDs:   UUIDArray{teacherMap["AH"]},
			ClassIDs:     UUIDArray{classMap["Grade 10 - A"]},
			CountPerWeek: 4,
		},
		// Mathematics for Grade 10-B: Dr. Ahmed Hassan, 4 periods/week
		{
			SubjectID:    subjectMap["Mathematics"],
			TeacherIDs:   UUIDArray{teacherMap["AH"]},
			ClassIDs:     UUIDArray{classMap["Grade 10 - B"]},
			CountPerWeek: 4,
		},
		// Physics for Grade 11 Science: Mr. Omar Farid, 3 periods/week
		{
			SubjectID:    subjectMap["Physics"],
			TeacherIDs:   UUIDArray{teacherMap["OF"]},
			ClassIDs:     UUIDArray{classMap["Grade 11 - Science"]},
			CountPerWeek: 3,
		},
		// Chemistry Lab for Grade 11 Science: Dr. Layla Mostafa, 2 periods/week (requires lab)
		{
			SubjectID:    subjectMap["Chemistry Lab"],
			TeacherIDs:   UUIDArray{teacherMap["LM"]},
			ClassIDs:     UUIDArray{classMap["Grade 11 - Science"]},
			CountPerWeek: 2,
		},
		// Arabic Language for Grade 10-A: Ms. Sara El-Sayed, 3 periods/week
		{
			SubjectID:    subjectMap["Arabic Language"],
			TeacherIDs:   UUIDArray{teacherMap["SE"]},
			ClassIDs:     UUIDArray{classMap["Grade 10 - A"]},
			CountPerWeek: 3,
		},
		// English Language for Grade 10-B: Mr. Karim Nour, 3 periods/week
		{
			SubjectID:    subjectMap["English Language"],
			TeacherIDs:   UUIDArray{teacherMap["KN"]},
			ClassIDs:     UUIDArray{classMap["Grade 10 - B"]},
			CountPerWeek: 3,
		},
		// Biology Lab for Grade 11 Arts: Dr. Layla Mostafa + Mr. Omar Farid (co-teach), 2/week
		{
			SubjectID:    subjectMap["Biology Lab"],
			TeacherIDs:   UUIDArray{teacherMap["LM"], teacherMap["OF"]},
			ClassIDs:     UUIDArray{classMap["Grade 11 - Arts"]},
			CountPerWeek: 2,
		},
		// English for Grade 11 Arts: Mr. Karim Nour, 3 periods/week
		{
			SubjectID:    subjectMap["English Language"],
			TeacherIDs:   UUIDArray{teacherMap["KN"]},
			ClassIDs:     UUIDArray{classMap["Grade 11 - Arts"]},
			CountPerWeek: 3,
		},
		// Arabic for Grade 10-B: Ms. Sara El-Sayed, 3 periods/week
		{
			SubjectID:    subjectMap["Arabic Language"],
			TeacherIDs:   UUIDArray{teacherMap["SE"]},
			ClassIDs:     UUIDArray{classMap["Grade 10 - B"]},
			CountPerWeek: 3,
		},
	}
	for i := range lessons {
		lessons[i].ID = uuid.New()
	}
	db.Create(&lessons)

	log.Println("[Seed] Demo data seeded successfully.")
}

// getEnv is a helper that returns the environment variable value for key,
// or fallback if the variable is not set.
func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
