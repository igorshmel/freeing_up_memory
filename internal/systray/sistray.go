package systray

import (
	"context"
	"fmt"
	"freeing_up_memory/internal/monitor"
	"freeing_up_memory/internal/ui"

	tray "github.com/getlantern/systray"
)

func InitializeTray(ctx context.Context, m *monitor.Monitor, cancel context.CancelFunc) {
	tray.Run(func() {
		mark := tray.AddMenuItem("Отметить подвисание", "Записать время нажатия для сопоставления с метриками")
		exit := tray.AddMenuItem("Exit", "Exit application")
		go func() {
			for {
				select {
				case <-ctx.Done():
					tray.Quit()
					return
				case <-exit.ClickedCh:
					cancel()
					tray.Quit()
					return
				case <-mark.ClickedCh:
					m.MarkStall()
				case s := <-m.Updates:
					if s.RAM == nil {
						tray.SetTitle("Err")
						continue
					}
					tray.SetTitle("")
					tray.SetIcon(ui.GenerateIconWithText(fmt.Sprintf("%.1f", float64(s.RAM.Free)/(1<<30))))
				}
			}
		}()
	}, cancel)
}
