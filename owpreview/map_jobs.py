"""One background map export with short, independently locked status reads."""
import copy
import threading
import time
import uuid


class MapExportJobs:
    def __init__(self):
        self._lock = threading.Lock()
        self._closed = False
        self._started = None
        self._finished = None
        self._thread = None
        self._job = dict(id=None, state='Idle', busy=False, message='',
                         completed=None, total=None, result=None, error=None)

    def _snapshot(self):
        snapshot = copy.deepcopy(self._job)
        elapsed = 0 if self._started is None else (self._finished or time.monotonic()) - self._started
        snapshot['elapsedSeconds'] = round(max(0, elapsed), 3)
        return snapshot

    def snapshot(self, job_id=None):
        with self._lock:
            if job_id is not None and job_id != self._job['id']:
                raise ValueError('This map export job is no longer available.')
            return self._snapshot()

    def _begin(self):
        with self._lock:
            if self._closed:
                raise ValueError('The application is closing. Start the map export after reopening it.')
            if self._job['busy']:
                raise ValueError('A map export is already running.')
            self._started, self._finished = time.monotonic(), None
            self._job = dict(id=uuid.uuid4().hex, state='Running', busy=True,
                             message='Loading map…', completed=None, total=None,
                             result=None, error=None)
            return self._snapshot()

    def _progress(self, job_id, message, completed=None, total=None):
        with self._lock:
            if self._job['id'] == job_id and self._job['busy']:
                self._job.update(message=str(message), completed=completed, total=total)

    def _finish(self, job_id, *, result=None, error=None):
        with self._lock:
            if self._job['id'] != job_id:
                return
            self._finished = time.monotonic()
            self._job.update(state='Failed' if error is not None else 'Completed', busy=False,
                             message=error if error is not None else 'Map export completed.',
                             result=copy.deepcopy(result), error=error)

    def _execute(self, job_id, operation, *, propagate=False):
        try:
            result = operation(lambda message, completed=None, total=None:
                               self._progress(job_id, message, completed, total))
        except Exception as error:
            self._finish(job_id, error=str(error) or type(error).__name__)
            if propagate:
                raise
            return None
        self._finish(job_id, result=result)
        return result

    def start(self, operation):
        snapshot = self._begin()
        worker = threading.Thread(target=self._execute, args=(snapshot['id'], operation),
                                  name='avolition-map-export', daemon=True)
        self._thread = worker
        try:
            worker.start()
        except Exception as error:
            self._finish(snapshot['id'], error=str(error) or type(error).__name__)
            raise
        # Return the accepted job even if a tiny export completed immediately.
        return snapshot

    def run(self, operation):
        """Compatibility path shares the same guard and observable status."""
        snapshot = self._begin()
        return self._execute(snapshot['id'], operation, propagate=True)

    def close(self):
        # The desktop asks the user to wait before closing an active export.
        # Do not join a native bridge operation indefinitely during forced exit.
        with self._lock:
            self._closed = True
