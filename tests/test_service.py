import logging

from auto_model_key_router.service import AccessLogLevelFilter


def test_access_log_level_filter_maps_http_failures() -> None:
    record = logging.LogRecord(
        "uvicorn.access", logging.INFO, __file__, 1, "request", (), None
    )
    record.status_code = 404

    assert AccessLogLevelFilter().filter(record)
    assert record.levelno == logging.WARNING
    assert record.levelname == "WARNING"

    record.status_code = 503
    assert AccessLogLevelFilter().filter(record)
    assert record.levelno == logging.ERROR
    assert record.levelname == "ERROR"


def test_access_log_level_filter_keeps_successful_responses() -> None:
    record = logging.LogRecord(
        "uvicorn.access", logging.INFO, __file__, 1, "request", (), None
    )
    record.status_code = 200

    assert AccessLogLevelFilter().filter(record)
    assert record.levelno == logging.INFO
