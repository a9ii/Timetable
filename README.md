<div align="center">
  <img src="https://img.shields.io/badge/Golang-1.22+-00ADD8?style=for-the-badge&logo=go" alt="Golang" />
  <img src="https://img.shields.io/badge/PostgreSQL-Neon-336791?style=for-the-badge&logo=postgresql" alt="PostgreSQL" />
  <img src="https://img.shields.io/badge/Fiber-v2.52-000000?style=for-the-badge&logo=go" alt="Fiber" />
  <img src="https://img.shields.io/badge/Vanilla_JS-ES6-F7DF1E?style=for-the-badge&logo=javascript" alt="JavaScript" />

  <h1>🎓 Smart Timetable CSP Solver</h1>
  <p>An enterprise-grade, blazing-fast school timetable generator built in Go. Featuring an advanced Backtracking Constraint Satisfaction Problem (CSP) engine, real-time WebSocket streaming, and a stunning glassmorphic UI.</p>
</div>

---

## ⚡ Overview

The **Smart Timetable** system completely automates the agonizing process of school scheduling. By ingesting raw school requirements (Teachers, Classes, Subjects, and Periods), the custom Go-based Constraint Satisfaction Engine systematically explores millions of combinations to generate a perfect, collision-free timetable in milliseconds. 

It natively supports complex business constraints such as **Morning/Evening Shifts**, **Teacher Availability Days**, and **Load Balancing**.

## ✨ Key Features

### 🧠 Advanced Solver Engine (Go)
- **Constraint Satisfaction Problem (CSP)**: Backtracking algorithmic engine ensuring zero collisions (teachers, classes, or rooms).
- **Heuristics**: Employs MCV (Most Constrained Variable) to sort tasks and LCV (Least Constraining Value) logic.
- **Load Balancing**: "Least Used Day First" heuristic evenly distributes classes across the week to prevent student burnout.
- **Student-Friendly Compact Scheduling**: Dynamically prioritizes earlier periods sequentially to prevent awkward gaps and late dismissals.
- **Shift Strictness**: Completely isolates "Morning" classes from "Evening" periods natively in the algorithmic loops.

### 🌐 Modern Architecture & API
- **Blazing Fast Backend**: Built on the [Fiber](https://gofiber.io/) framework for rapid routing and low-memory footprints.
- **Real-Time WebSockets**: Live progress streaming directly to the browser UI during the generation loops.
- **Interactive Drag-and-Drop**: A custom endpoint (`PATCH /api/cards`) performs robust backend validation to allow manual schedule tweaks without breaking strict constraints.

### 🎨 Premium Glassmorphic UI
- **Zero-Dependency Frontend**: Written in modular Vanilla JS (ES6) with HTML/TailwindCSS for ultimate performance.
- **Stunning Aesthetics**: Apple-inspired glassmorphism, dynamic gradients, blur layers, and interactive micro-animations.
- **Standalone Matrix Viewer**: Provides a dedicated, read-only matrix grid view specifically designed for students and staff.

### 📥 aSc Importer Integration
Includes a built-in CLI tool (`cmd/importer/main.go`) capable of seamlessly ingesting `Data.json` exports from standard **aSc Timetables** software, featuring automatic deduplication and shift parsing.

---

## 🚀 Getting Started

### Prerequisites
- **Go** (1.22 or higher)
- **PostgreSQL** Database (Neon or local)

### 1. Environment Setup
Clone the repository and configure your environment:
```bash
git clone https://github.com/a9ii/Timetable.git
cd Timetable
```
Create a `.env` file in the root directory:
```env
# Example .env configuration
DATABASE_URL=postgres://user:password@hostname:5432/timetable?sslmode=require
PORT=8080
```

### 2. Import Data (Optional)
If you have an aSc Timetables `Data.json` export in the root folder, use the importer to automatically seed the database:
```bash
go run ./cmd/importer/main.go
```

### 3. Run the Server
Boot the Go Fiber web server:
```bash
go run .
```

### 4. Access the Dashboard
- **Admin Dashboard**: `http://localhost:8080/` (Manage constraints, generate timetables, drag-and-drop).
- **Student View**: `http://localhost:8080/timetable.html` (Read-only matrix).

---

## 🏗️ Architecture Design

- **Models**: Handled by `GORM` utilizing robust PostgreSQL structures (including custom `StringArray` and `UUIDArray` scanners for advanced relational data).
- **Solver Loop**: Triggered asynchronously via a Goroutine to prevent HTTP blocking. Emits JSON progress events via `ProgressEvent` channels mapped to a global `sync.Map`.
- **Frontend Sync**: The UI uses standard `fetch()` API combined with `Map` data structures for `O(1)` relational lookups (e.g., retrieving a Teacher's name by UUID).

## 🛡️ License

This project is licensed under the MIT License. Feel free to fork, modify, and distribute.
