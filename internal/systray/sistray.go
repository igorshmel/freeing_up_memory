package systray

import (
	"fmt"
	"freeing_up_memory/internal/memory"
	"freeing_up_memory/internal/ui"
	"github.com/getlantern/systray"
	tray "github.com/getlantern/systray"
	"log"
	"time"
)

func InitializeTray() {
	systray.Run(onReady, func() {})
}

func onReady() {
	freeMem, err := memory.GetFreeMemory()
	if err != nil {
		log.Println("Error fetching memory:", err)
		tray.SetTitle("Err")
	} else {
		tray.SetIcon(ui.GenerateIconWithText(fmt.Sprintf("%.1f", float64(freeMem)/1024/1024/1024)))
	}

	exitItem := tray.AddMenuItem("Exit", "Exit application")

	go func() {
		for {
			freeMem, err := memory.GetFreeMemory()
			if err != nil {
				log.Println("Error fetching memory:", err)
				tray.SetTitle("Err")
			} else {
				tray.SetIcon(ui.GenerateIconWithText(fmt.Sprintf("%.1f", float64(freeMem)/1024/1024/1024)))

				if freeMem < memory.Threshold {
					log.Printf("Memory below threshold(%.1fG), clearing cache...", float64(memory.Threshold)/1024/1024/1024)
					if err := memory.ClearCache(); err != nil {
						log.Println("Error clearing cache:", err)
					}
				}
			}
			time.Sleep(60 * time.Second)
		}
	}()
	go func() {
		<-exitItem.ClickedCh
		tray.Quit()
	}()
}
