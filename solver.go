// =============================================================================
// FILE: solver.go  (REWRITTEN)
// PACKAGE: main
//
// ALGORITHM CHANGE:
//   The original pure recursive backtracking is O(30^411) in the worst case —
//   it predictably stalls on large real-world datasets.
//
//   Replacement: ITERATIVE GREEDY + MULTI-ATTEMPT with shuffled slot ordering.
//
//   For each attempt:
//     1. Iterate tasks in MCV (most-constrained-first) order.
//     2. For each task, iterate (day × period) in a shuffled order.
//     3. Place the task in the first valid slot found.
//     4. If no slot is valid for a task → attempt fails → reset & retry with
//        a different shuffle seed.
//
//   Complexity per attempt: O(N × D × P) = O(411 × 5 × 6) ≈ 12,000 ops.
//   Max attempts: 50.  Typical success: attempt 1–3.
//
//   This approach is standard in OR/scheduling literature and handles real
//   school-sized datasets (hundreds of lessons) in milliseconds.
// =============================================================================
package main

import (
	"fmt"
	"log"
	"math/rand"
	"sort"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// =============================================================================
// PROGRESS STREAMING TYPES
// =============================================================================

type ProgressEventType string

const (
	ProgressLog       ProgressEventType = "log"
	ProgressUpdate    ProgressEventType = "progress"
	ProgressCompleted ProgressEventType = "completed"
	ProgressFailed    ProgressEventType = "failed"
	ProgressPing      ProgressEventType = "ping"
)

type ProgressEvent struct {
	Type       ProgressEventType `json:"type"`
	Percentage int               `json:"percentage"`
	Message    string            `json:"message"`
	CardsCount int               `json:"cardsCount,omitempty"`
}

// =============================================================================
// SOLVER INTERNAL TYPES
// =============================================================================

type SolverTask struct {
	Lesson   Lesson
	Subject  Subject
	Priority int
}

type slotCombo struct {
	DayID    string
	PeriodID string
}

func slotKey(dayID, periodID string) string {
	return dayID + "|" + periodID
}

// =============================================================================
// PUBLIC ENTRY POINT
// =============================================================================

func RunSolver(db *gorm.DB, progress chan<- ProgressEvent) {
	defer close(progress)

	emit := func(t ProgressEventType, pct int, msg string) {
		select {
		case progress <- ProgressEvent{Type: t, Percentage: pct, Message: msg}:
		default:
		}
	}

	emit(ProgressLog, 0, "🚀 Solver engine initialised.")
	emit(ProgressUpdate, 2, "Loading master data from database...")

	// -------------------------------------------------------------------------
	// STEP 1 — Load all master data into memory
	// -------------------------------------------------------------------------
	var days []DayDef
	if err := db.Order("\"index\" ASC").Find(&days).Error; err != nil {
		emit(ProgressFailed, 0, "Failed to load days: "+err.Error())
		return
	}

	var periods []Period
	if err := db.Order("\"index\" ASC").Find(&periods).Error; err != nil {
		emit(ProgressFailed, 0, "Failed to load periods: "+err.Error())
		return
	}

	var classrooms []Classroom
	if err := db.Find(&classrooms).Error; err != nil {
		emit(ProgressFailed, 0, "Failed to load classrooms: "+err.Error())
		return
	}

	var classes []Class
	if err := db.Find(&classes).Error; err != nil {
		emit(ProgressFailed, 0, "Failed to load classes: "+err.Error())
		return
	}

	var teachers []Teacher
	if err := db.Find(&teachers).Error; err != nil {
		emit(ProgressFailed, 0, "Failed to load teachers: "+err.Error())
		return
	}

	var lessons []Lesson
	if err := db.Preload("Subject").Find(&lessons).Error; err != nil {
		emit(ProgressFailed, 0, "Failed to load lessons: "+err.Error())
		return
	}

	emit(ProgressLog, 10, fmt.Sprintf(
		"✅ Loaded: %d days, %d periods, %d rooms, %d classes, %d teachers, %d lessons.",
		len(days), len(periods), len(classrooms), len(classes), len(teachers), len(lessons),
	))

	if len(days) == 0 || len(periods) == 0 {
		emit(ProgressFailed, 0, "Cannot schedule: no days or periods configured.")
		return
	}
	if len(lessons) == 0 {
		emit(ProgressFailed, 0, "No lessons defined.")
		return
	}

	// -------------------------------------------------------------------------
	// STEP 2 — Flatten + MCV sort
	// -------------------------------------------------------------------------
	emit(ProgressUpdate, 15, "Flattening and sorting tasks (MCV heuristic)...")
	tasks := flattenAndSort(lessons)
	total := len(tasks)
	emit(ProgressLog, 18, fmt.Sprintf("📋 %d individual slots to schedule.", total))

	// -------------------------------------------------------------------------
	// STEP 3 — Build all (day × period) slot combinations
	// -------------------------------------------------------------------------
	combos := make([]slotCombo, 0, len(days)*len(periods))
	for _, d := range days {
		for _, p := range periods {
			combos = append(combos, slotCombo{d.ID, p.ID})
		}
	}

	// -------------------------------------------------------------------------
	// STEP 4 — Multi-attempt greedy solver
	// -------------------------------------------------------------------------
	const maxAttempts = 50
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))
	startTime := time.Now()

	var resultCards []Card

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		emit(ProgressUpdate, 20, fmt.Sprintf("⚙️  Attempt %d / %d ...", attempt, maxAttempts))

		cards, ok := greedyAttempt(tasks, combos, classrooms, classes, periods, teachers, total, attempt, rng, progress)
		if ok {
			resultCards = cards
			emit(ProgressLog, 88, fmt.Sprintf(
				"✅ Solution found on attempt %d in %s.",
				attempt, time.Since(startTime).Round(time.Millisecond),
			))
			break
		}

		emit(ProgressLog, 20+attempt, fmt.Sprintf(
			"↩️  Attempt %d failed — reshuffling and retrying...", attempt,
		))

		if attempt == maxAttempts {
			emit(ProgressFailed, 0, fmt.Sprintf(
				"❌ Could not schedule all %d slots after %d attempts (%s). "+
					"Try reducing CountPerWeek on some lessons or adding more periods/days.",
				total, maxAttempts, time.Since(startTime).Round(time.Millisecond),
			))
			return
		}
	}

	// -------------------------------------------------------------------------
	// STEP 5 — Commit to database
	// -------------------------------------------------------------------------
	emit(ProgressUpdate, 90, "💾 Committing timetable to database...")

	err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Session(&gorm.Session{AllowGlobalUpdate: true}).Delete(&Card{}).Error; err != nil {
			return fmt.Errorf("clear cards: %w", err)
		}
		const batch = 100
		for i := 0; i < len(resultCards); i += batch {
			end := i + batch
			if end > len(resultCards) {
				end = len(resultCards)
			}
			if err := tx.Create(resultCards[i:end]).Error; err != nil {
				return fmt.Errorf("insert cards batch: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		emit(ProgressFailed, 0, "Database transaction failed: "+err.Error())
		return
	}

	elapsed := time.Since(startTime).Round(time.Millisecond)
	log.Printf("[Solver] Committed %d cards in %s.", len(resultCards), elapsed)
	emit(ProgressUpdate, 100, "")
	emit(ProgressCompleted, 100, fmt.Sprintf(
		"🎉 Timetable generated! %d cards scheduled in %s.", len(resultCards), elapsed,
	))
}

// =============================================================================
// GREEDY ATTEMPT
// =============================================================================

// greedyAttempt tries to schedule all tasks in one pass.
// On the first attempt the combos are in natural order; subsequent attempts
// use a freshly shuffled copy so we explore different slot orderings.
// Returns (cards, true) on full success, (nil, false) if any task is unplaceable.
func greedyAttempt(
	tasks []SolverTask,
	combos []slotCombo,
	classrooms []Classroom,
	classes []Class,
	periods []Period,
	teachers []Teacher,
	total int,
	attempt int,
	rng *rand.Rand,
	progress chan<- ProgressEvent,
) ([]Card, bool) {

	// Shuffle combo order for all attempts after the first.
	localCombos := make([]slotCombo, len(combos))
	copy(localCombos, combos)
	if attempt > 1 {
		rng.Shuffle(len(localCombos), func(i, j int) {
			localCombos[i], localCombos[j] = localCombos[j], localCombos[i]
		})
	}

	classMap := make(map[uuid.UUID]Class, len(classes))
	for _, c := range classes {
		classMap[c.ID] = c
	}

	periodMap := make(map[string]Period, len(periods))
	for _, p := range periods {
		periodMap[p.ID] = p
	}

	teacherMap := make(map[uuid.UUID]Teacher, len(teachers))
	for _, t := range teachers {
		teacherMap[t.ID] = t
	}

	classLessonsPerDay := make(map[uuid.UUID]map[string]int)
	for _, c := range classes {
		classLessonsPerDay[c.ID] = make(map[string]int)
	}

	// Occupancy maps  key = "dayID|periodID"
	teacherBusy := make(map[string]map[uuid.UUID]bool)
	classBusy   := make(map[string]map[uuid.UUID]bool)
	roomBusy    := make(map[string]map[uuid.UUID]bool)
	for _, c := range localCombos {
		k := slotKey(c.DayID, c.PeriodID)
		teacherBusy[k] = make(map[uuid.UUID]bool)
		classBusy[k]   = make(map[uuid.UUID]bool)
		roomBusy[k]    = make(map[uuid.UUID]bool)
	}

	cards := make([]Card, 0, total)

	for i, task := range tasks {
		// Emit progress every 10 tasks
		if i%10 == 0 {
			pct := 20 + int(float64(i)/float64(total)*65)
			select {
			case progress <- ProgressEvent{
				Type:       ProgressUpdate,
				Percentage: pct,
				Message:    fmt.Sprintf("Scheduling slot %d / %d ...", i+1, total),
			}:
			default:
			}
		}

		placed := false
		
		// 1. LOAD BALANCING (Least Used Day First Heuristic)
		// Dynamically sort localCombos for this specific task based on the class's current load per day.
		sort.SliceStable(localCombos, func(i, j int) bool {
			dayI := localCombos[i].DayID
			dayJ := localCombos[j].DayID
			loadI, loadJ := 0, 0
			for _, cid := range task.Lesson.ClassIDs {
				if classLessonsPerDay[cid][dayI] > loadI {
					loadI = classLessonsPerDay[cid][dayI]
				}
				if classLessonsPerDay[cid][dayJ] > loadJ {
					loadJ = classLessonsPerDay[cid][dayJ]
				}
			}
			if loadI != loadJ {
				return loadI < loadJ
			}

			// 2. STUDENT-FRIENDLY COMPACT SCHEDULING
			// If days have equal load, prioritize earlier periods (lower Index) 
			// so that schedules are compact and students leave as early as possible.
			periodI := periodMap[localCombos[i].PeriodID]
			periodJ := periodMap[localCombos[j].PeriodID]
			return periodI.Index < periodJ.Index
		})

		for _, combo := range localCombos {
			k := slotKey(combo.DayID, combo.PeriodID)

			// --- Strict Shift Matching ---
			// A class can only be scheduled in a period if their shifts match (or if one has no specific shift).
			invalidPeriod := false
			targetPeriod := periodMap[combo.PeriodID]
			for _, cid := range task.Lesson.ClassIDs {
				cls := classMap[cid]
				if cls.Shift != "" && targetPeriod.Shift != "" && cls.Shift != targetPeriod.Shift {
					invalidPeriod = true
					break
				}
			}
			if invalidPeriod {
				continue
			}

			// --- Specific Teacher Availability Days ---
			invalidDay := false
			for _, tid := range task.Lesson.TeacherIDs {
				teacher := teacherMap[tid]
				if len(teacher.AvailableDayIDs) > 0 {
					dayAllowed := false
					for _, allowedDay := range teacher.AvailableDayIDs {
						if allowedDay == combo.DayID {
							dayAllowed = true
							break
						}
					}
					if !dayAllowed {
						invalidDay = true
						break
					}
				}
			}
			if invalidDay {
				continue
			}

			// --- Teacher conflict ---
			conflict := false
			for _, tid := range task.Lesson.TeacherIDs {
				if teacherBusy[k][tid] {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}

			// --- Class conflict ---
			for _, cid := range task.Lesson.ClassIDs {
				if classBusy[k][cid] {
					conflict = true
					break
				}
			}
			if conflict {
				continue
			}

			// --- Room assignment ---
			roomID := findAvailableRoom(classrooms, roomBusy[k], task.Subject.RequiresLab)
			// For non-lab subjects a nil room is acceptable.
			if task.Subject.RequiresLab && roomID == nil {
				continue
			}

			// --- Place ---
			for _, tid := range task.Lesson.TeacherIDs {
				teacherBusy[k][tid] = true
			}
			for _, cid := range task.Lesson.ClassIDs {
				classBusy[k][cid] = true
				classLessonsPerDay[cid][combo.DayID]++
			}
			if roomID != nil {
				roomBusy[k][*roomID] = true
			}

			cards = append(cards, Card{
				ID:          uuid.New(),
				LessonID:    task.Lesson.ID,
				PeriodID:    combo.PeriodID,
				DayDefID:    combo.DayID,
				ClassroomID: roomID,
			})
			placed = true
			break
		}

		if !placed {
			return nil, false
		}
	}

	return cards, true
}

// =============================================================================
// HELPERS
// =============================================================================

func flattenAndSort(lessons []Lesson) []SolverTask {
	var tasks []SolverTask
	for _, l := range lessons {
		p := calcPriority(l)
		for i := 0; i < l.CountPerWeek; i++ {
			tasks = append(tasks, SolverTask{
				Lesson:  l,
				Subject: l.Subject,
				Priority: p,
			})
		}
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		return tasks[i].Priority > tasks[j].Priority
	})
	return tasks
}

func calcPriority(l Lesson) int {
	score := l.CountPerWeek
	if l.Subject.RequiresLab {
		score += 50
	}
	if len(l.TeacherIDs) >= 2 {
		score += 30
	}
	if len(l.ClassIDs) >= 2 {
		score += 10
	}
	return score
}

func findAvailableRoom(rooms []Classroom, busy map[uuid.UUID]bool, needsLab bool) *uuid.UUID {
	for i := range rooms {
		r := &rooms[i]
		if r.IsLab != needsLab {
			continue
		}
		if busy[r.ID] {
			continue
		}
		id := r.ID
		return &id
	}
	if !needsLab {
		return nil // no room needed, nil is fine
	}
	return nil
}
