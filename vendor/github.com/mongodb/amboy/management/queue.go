package management

import (
	"context"
	"time"

	"github.com/mongodb/amboy"
	"github.com/pkg/errors"
)

type queueManager struct {
	queue amboy.Queue
}

// NewQueueManager returns a queue manager that provides the supported
// Management interface by calling the output of amboy.Queue.Results() and
// amboy.Queue.JobStats(), iterating over jobs directly. Use this to manage
// in-memory queue implementations more generically.
//
// The management algorithms may impact performance of jobs, as queues may
// require some locking to their Jobs function. Additionally, the speed of
// these operations will necessarily degrade with the number of jobs. Do pass
// contexts with timeouts to in these cases.
func NewQueueManager(q amboy.Queue) Management {
	return &queueManager{
		queue: q,
	}
}

func (m *queueManager) JobStatus(ctx context.Context, f CounterFilter) (*JobStatusReport, error) {
	if err := f.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	counters := map[string]int{}
	switch f {
	case InProgress:
		for stat := range m.queue.JobStats(ctx) {
			if stat.InProgress {
				job, ok := m.queue.Get(ctx, stat.ID)
				if ok {
					counters[job.Type().Name]++
				}
			}
		}
	case Pending:
		for stat := range m.queue.JobStats(ctx) {
			if !stat.Completed && !stat.InProgress {
				job, ok := m.queue.Get(ctx, stat.ID)
				if ok {
					counters[job.Type().Name]++
				}
			}
		}
	case Stale:
		for stat := range m.queue.JobStats(ctx) {
			if !stat.Completed && stat.InProgress && time.Since(stat.ModificationTime) > amboy.LockTimeout {
				job, ok := m.queue.Get(ctx, stat.ID)
				if ok {
					counters[job.Type().Name]++
				}
			}
		}
	}

	out := JobStatusReport{}

	for jt, num := range counters {
		out.Stats = append(out.Stats, JobCounters{
			ID:    jt,
			Count: num,
		})
	}

	out.Filter = string(f)

	return &out, nil
}

func (m *queueManager) RecentTiming(ctx context.Context, window time.Duration, f RuntimeFilter) (*JobRuntimeReport, error) {
	var err error

	if err = f.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}

	if window <= time.Second {
		return nil, errors.New("must specify windows greater than one second")
	}

	counters := map[string][]time.Duration{}

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	switch f {
	case Duration:
		for job := range m.queue.Results(ctx) {
			stat := job.Status()
			ti := job.TimeInfo()
			if stat.Completed && time.Since(ti.End) < window {
				jt := job.Type().Name
				counters[jt] = append(counters[jt], ti.End.Sub(ti.Start))
			}
		}
	case Latency:
		for stat := range m.queue.JobStats(ctx) {
			job, ok := m.queue.Get(ctx, stat.ID)
			if !ok {
				continue
			}
			ti := job.TimeInfo()
			if !stat.Completed && time.Since(ti.End) < window {
				jt := job.Type().Name
				counters[jt] = append(counters[jt], time.Since(ti.Created))
			}
		}
	case Running:
		for stat := range m.queue.JobStats(ctx) {
			if !stat.Completed && stat.InProgress {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}
				ti := job.TimeInfo()
				jt := job.Type().Name
				counters[jt] = append(counters[jt], time.Since(ti.Start))
			}
		}
	default:
		return nil, errors.New("invalid job runtime filter")
	}

	runtimes := []JobRuntimes{}

	for k, v := range counters {
		var total time.Duration
		for _, i := range v {
			total += i
		}

		runtimes = append(runtimes, JobRuntimes{
			ID:       k,
			Duration: total / time.Duration(len(v)),
		})
	}

	return &JobRuntimeReport{
		Filter: string(f),
		Period: window,
		Stats:  runtimes,
	}, nil
}

func (m *queueManager) JobIDsByState(ctx context.Context, jobType string, f CounterFilter) (*JobReportIDs, error) {
	var err error
	if err = f.Validate(); err != nil {
		return nil, errors.WithStack(err)
	}

	// it might be the case that we shold use something with
	// set-ish properties if queues return the same job more than
	// once, and it poses a problem.
	var ids []string

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	switch f {
	case InProgress:
		for stat := range m.queue.JobStats(ctx) {
			if jobType != "" {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok && job.Type().Name != jobType {
					continue
				}
			}
			if stat.InProgress {
				ids = append(ids, stat.ID)
			}
		}
	case Pending:
		for stat := range m.queue.JobStats(ctx) {
			if jobType != "" {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok && job.Type().Name != jobType {
					continue
				}
			}
			if !stat.Completed && !stat.InProgress {
				ids = append(ids, stat.ID)
			}
		}
	case Stale:
		for stat := range m.queue.JobStats(ctx) {
			if jobType != "" {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok && job.Type().Name != jobType {
					continue
				}
			}
			if !stat.Completed && stat.InProgress && time.Since(stat.ModificationTime) > amboy.LockTimeout {
				ids = append(ids, stat.ID)
			}
		}
	default:
		return nil, errors.New("invalid job status filter")
	}

	return &JobReportIDs{
		Filter: string(f),
		Type:   jobType,
		IDs:    ids,
	}, nil
}

func (m *queueManager) RecentErrors(ctx context.Context, window time.Duration, f ErrorFilter) (*JobErrorsReport, error) {
	var err error
	if err = f.Validate(); err != nil {
		return nil, errors.WithStack(err)

	}
	if window <= time.Second {
		return nil, errors.New("must specify windows greater than one second")
	}

	collector := map[string]JobErrorsForType{}

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	switch f {
	case UniqueErrors:
		for stat := range m.queue.JobStats(ctx) {
			if stat.Completed && stat.ErrorCount > 0 {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}

				ti := job.TimeInfo()
				if time.Since(ti.End) > window {
					continue
				}
				jt := job.Type().Name

				val := collector[jt]

				val.Count++
				val.Total += stat.ErrorCount
				val.Errors = append(val.Errors, stat.Errors...)
				collector[jt] = val
			}
		}
		for k, v := range collector {
			errs := map[string]struct{}{}

			for _, e := range v.Errors {
				errs[e] = struct{}{}
			}

			v.Errors = []string{}
			for e := range errs {
				v.Errors = append(v.Errors, e)
			}

			collector[k] = v
		}
	case AllErrors:
		for stat := range m.queue.JobStats(ctx) {
			if stat.Completed && stat.ErrorCount > 0 {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}

				ti := job.TimeInfo()
				if time.Since(ti.End) > window {
					continue
				}
				jt := job.Type().Name

				val := collector[jt]

				val.Count++
				val.Total += stat.ErrorCount
				val.Errors = append(val.Errors, stat.Errors...)
				collector[jt] = val
			}
		}
	case StatsOnly:
		for stat := range m.queue.JobStats(ctx) {
			if stat.Completed && stat.ErrorCount > 0 {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}

				ti := job.TimeInfo()
				if time.Since(ti.End) > window {
					continue
				}
				jt := job.Type().Name

				val := collector[jt]

				val.Count++
				val.Total += stat.ErrorCount
				collector[jt] = val
			}
		}
	default:
		return nil, errors.New("operation is not supported")
	}

	var reports []JobErrorsForType

	for k, v := range collector {
		v.ID = k
		v.Average = float64(v.Total / v.Count)

		reports = append(reports, v)
	}

	return &JobErrorsReport{
		Period:         window,
		FilteredByType: false,
		Data:           reports,
	}, nil
}

func (m *queueManager) RecentJobErrors(ctx context.Context, jobType string, window time.Duration, f ErrorFilter) (*JobErrorsReport, error) {
	var err error
	if err = f.Validate(); err != nil {
		return nil, errors.WithStack(err)

	}
	if window <= time.Second {
		return nil, errors.New("must specify windows greater than one second")
	}

	collector := map[string]JobErrorsForType{}

	var cancel context.CancelFunc
	ctx, cancel = context.WithCancel(ctx)
	defer cancel()

	switch f {
	case UniqueErrors:
		for stat := range m.queue.JobStats(ctx) {
			if stat.Completed && stat.ErrorCount > 0 {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}
				if job.Type().Name != jobType {
					continue
				}

				ti := job.TimeInfo()
				if time.Since(ti.End) > window {
					continue
				}

				val := collector[jobType]

				val.Count++
				val.Total += stat.ErrorCount
				val.Errors = append(val.Errors, stat.Errors...)
				collector[jobType] = val
			}
		}
		for k, v := range collector {
			errs := map[string]struct{}{}

			for _, e := range v.Errors {
				errs[e] = struct{}{}
			}

			v.Errors = []string{}
			for e := range errs {
				v.Errors = append(v.Errors, e)
			}

			collector[k] = v
		}
	case AllErrors:
		for stat := range m.queue.JobStats(ctx) {
			if stat.Completed && stat.ErrorCount > 0 {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}
				if job.Type().Name != jobType {
					continue
				}

				ti := job.TimeInfo()
				if time.Since(ti.End) > window {
					continue
				}

				val := collector[jobType]

				val.Count++
				val.Total += stat.ErrorCount
				val.Errors = append(val.Errors, stat.Errors...)
				collector[jobType] = val
			}
		}
	case StatsOnly:
		for stat := range m.queue.JobStats(ctx) {
			if stat.Completed && stat.ErrorCount > 0 {
				job, ok := m.queue.Get(ctx, stat.ID)
				if !ok {
					continue
				}
				if job.Type().Name != jobType {
					continue
				}

				ti := job.TimeInfo()
				if time.Since(ti.End) > window {
					continue
				}

				val := collector[jobType]

				val.Count++
				val.Total += stat.ErrorCount
				collector[jobType] = val
			}
		}
	default:
		return nil, errors.New("operation is not supported")
	}

	var reports []JobErrorsForType

	for k, v := range collector {
		v.ID = k
		v.Average = float64(v.Total / v.Count)

		reports = append(reports, v)
	}

	return &JobErrorsReport{
		Period:         window,
		FilteredByType: true,
		Data:           reports,
	}, nil

}

func (m *queueManager) CompleteJob(ctx context.Context, name string) error {
	j, exists := m.queue.Get(ctx, name)
	if !exists {
		return errors.Errorf("cannot recover job with name '%s'", name)
	}

	m.queue.Complete(ctx, j)
	return nil
}

func (m *queueManager) CompleteJobsByType(ctx context.Context, jobType string) error {
	for stat := range m.queue.JobStats(ctx) {
		if stat.Completed {
			continue
		}

		job, ok := m.queue.Get(ctx, stat.ID)
		if ok && job.Type().Name != jobType {
			continue
		}
		m.queue.Complete(ctx, job)
	}

	return nil
}
