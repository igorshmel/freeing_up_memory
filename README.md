# Freeing up memory
Freeing up memory for Linux Debian

This project monitors the system's free memory and clears the cache if the free memory drops below a defined threshold. It runs as a background system tray application using the `systray` library and displays the available memory in gigabytes.

## Features

- Monitors free memory every minute.
- If free memory is below a threshold (1GB), it automatically clears the cache.
- Displays the current memory usage in the system tray icon.
- Provides an "Exit" menu option in the system tray.

## Requirements

- Go 1.18+ (for building and running the project).
- `systray` package for system tray support.
- `gopsutil` package to retrieve memory usage information.
- A system supporting `exec.Command` for clearing cache (Linux).

## Installation

To install and run this project locally, follow these steps:

### 1. Clone the repository:

```bash
git clone https://github.com:igorshmel/freeing_up_memory.git
cd freeing_up_memory
```

### 2. Add font file:  

file: SFMono-Bold.ttf
path: freeing_up_memory/internal/ui/assets

### 2. Install dependencies:

```bash
go mod init
go mod tidy
```

### 3. Build the project:

```bash
cd freeing_up_memory/cmd/monitor
go build -o freeing_up_memory main.go
```

### 4. Run the application:

```bash
sudo ./freeing_up_memory
```

## How It Works

- The application checks the system's available memory every minute.
- If the free memory drops below 1GB, the application attempts to clear the system cache using the `sync` and `drop_caches` command on Linux.
- The current memory status is displayed in the system tray.
- You can exit the application by selecting "Exit" from the tray menu.

## License

This project is licensed under the  Apache License Version 2.0 - see the [LICENSE](LICENSE) file for details.
