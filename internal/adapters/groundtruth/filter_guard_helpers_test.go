package groundtruth_test

import "time"

// nowRFC3339 is a tiny helper used by the override-token test
// fixture to stamp a fresh timestamp into the token JSON.
func nowRFC3339() string {
	return time.Now().UTC().Format(time.RFC3339)
}
