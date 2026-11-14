package pack

import (
	"fmt"

	"github.com/FloareDor/perry/internal/object"
)

func fmtSscan(s string, v *int64) (int, error) { return fmt.Sscan(s, v) }

func parseIDForTest(s string) (object.ID, error) { return object.ParseID(s) }
