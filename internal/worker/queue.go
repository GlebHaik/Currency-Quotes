package worker

const defaultQueueSize = 64

// Job is one call to the provider
type Job struct {
	Pair     string
	IDs      []string
	Attempts int
}

//	QuoteService --wake--> Dispatcher --jobs--> WorkerPool
//
// Postgres stays the source of truth: anything lost from a channel is still pending
// in the table.
type JobQueue struct {
	jobs chan Job
	wake chan struct{}
}

func NewJobQueue() *JobQueue {
	return &JobQueue{
		jobs: make(chan Job, defaultQueueSize),
		wake: make(chan struct{}, 1),
	}
}

func (q *JobQueue) Notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}
