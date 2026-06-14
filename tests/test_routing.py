from auto_model_key_router.routing import RetryPolicy


def test_retry_policy_tries_each_available_key_for_automatic_routing() -> None:
    assert (
        RetryPolicy(max_retries=5).attempts(
            key_count=3,
            requested_key_name=None,
            only_first=False,
        )
        == 3
    )


def test_retry_policy_uses_configured_retries_for_fixed_routes() -> None:
    policy = RetryPolicy(max_retries=2)

    assert (
        policy.attempts(key_count=3, requested_key_name="key-a", only_first=False) == 3
    )
    assert policy.attempts(key_count=3, requested_key_name=None, only_first=True) == 3
    assert policy.attempts(key_count=1, requested_key_name=None, only_first=False) == 3
