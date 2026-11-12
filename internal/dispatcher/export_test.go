package dispatcher

// SetTickHook makes Run call hook after every tick, so a test can follow
// the loop without sleeping.
func SetTickHook(d *Dispatcher, hook func(TickResult)) { d.onTick = hook }
