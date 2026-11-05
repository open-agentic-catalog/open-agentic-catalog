package formal

import (
	"os"
	"reflect"
	"testing"
	"time"
)

// TestTimeModel binds the reviewed declarations to the repository Go toolchain.
func TestTimeModel(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("time.gobra")
	if err != nil {
		t.Fatal(err)
	}
	const model = "package time\n\ntype Duration int64\n\nconst Second Duration = 1000000000\nconst Hour Duration = 3600 * Second\n"
	if string(raw) != model || reflect.TypeOf(time.Duration(0)).Kind() != reflect.Int64 || int64(time.Second) != 1000000000 || int64(time.Hour) != 3600000000000 {
		t.Fatal("formal time model differs from reviewed Go declarations")
	}
}
