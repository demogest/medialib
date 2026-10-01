"""Background tasks (copy, move, delete, size of a folder ...) with progress that the UI polls."""
import itertools
import threading
import time

_ids = itertools.count(1)


class Cancelled(Exception):
    pass


class Task:
    def __init__(self, kind, title):
        self.id = f"t{next(_ids)}"
        self.kind, self.title = kind, title
        self.state = "running"          # running | done | error | cancelled
        self.done = self.total = 0      # items
        self.bytes = 0
        self.line = ""                  # what it is doing right now
        self.errors = []                # up to 50 messages about items that failed
        self.error_count = 0
        self.result = None
        self.started, self.finished = time.time(), None
        self._cancel, self._finished = threading.Event(), threading.Event()

    def check(self):
        """Call between steps: stops the task if the user asked for that."""
        if self._cancel.is_set():
            raise Cancelled()

    def fail(self, message):
        self.error_count += 1
        if len(self.errors) < 50:
            self.errors.append(message)

    def wait(self, seconds):
        return self._finished.wait(seconds)

    def to_dict(self):
        return {"id": self.id, "kind": self.kind, "title": self.title, "state": self.state, "done": self.done, "total": self.total,
                "bytes": self.bytes, "line": self.line, "errors": self.errors, "error_count": self.error_count, "result": self.result,
                "started": self.started, "finished": self.finished}


class Tasks:
    KEEP = 40  # finished tasks kept for the Activity list

    def __init__(self):
        self._tasks, self._lock = {}, threading.Lock()

    def start(self, kind, title, fn):
        """Run fn(task) on a thread. Returns the Task at once."""
        task = Task(kind, title)

        def work():
            try:
                fn(task)
                task.state = "error" if task.error_count and not task.done else "done"
            except Cancelled:
                task.state = "cancelled"
            except Exception as e:  # noqa: BLE001 - whatever went wrong is shown to the user
                task.state, task.line = "error", str(e)[:300]
                task.fail(str(e)[:300])
            task.finished = time.time()
            task._finished.set()

        with self._lock:
            self._tasks[task.id] = task
            finished = sorted((t for t in self._tasks.values() if t.finished), key=lambda t: t.finished)
            for old in finished[:-self.KEEP]:
                del self._tasks[old.id]
        threading.Thread(target=work, daemon=True, name=task.id).start()
        return task

    def get(self, task_id):
        return self._tasks.get(task_id)

    def list(self):
        with self._lock:
            return [t.to_dict() for t in sorted(self._tasks.values(), key=lambda t: -t.started)]

    def cancel(self, task_id):
        t = self._tasks.get(task_id)
        if t:
            t._cancel.set()
        return t

    def dismiss(self, task_id=None):
        """Forget one finished task, or all of them."""
        with self._lock:
            for t in [t for t in self._tasks.values() if t.finished and (task_id in (None, t.id))]:
                del self._tasks[t.id]
