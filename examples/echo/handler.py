"""The example broker's commands, which neurokit runs once per request.

No provider sits behind them, so set_greeting only pretends to save.
"""

from neurokit_handler import NOT_SENT, VERIFIED, Done, Fail


def echo(text, times=1):
    """Returns text repeated, the way a read returns a provider's data.

    Args:
        text: The text to repeat.
        times: How many copies, joined by spaces.
    """
    return {"text": " ".join([text] * times)}


def set_greeting(greeting):
    """Pretends to save greeting, the way a mutation reports its write.

    Args:
        greeting: The new greeting.

    Returns:
        The saved greeting, with VERIFIED, since the change is confirmed.

    Raises:
        Fail: blank-greeting with NOT_SENT, refused before any save.
    """
    if not greeting.strip():
        raise Fail("blank-greeting", "the greeting is blank", write=NOT_SENT)
    return Done({"greeting": greeting}, write=VERIFIED)


def check():
    """Runs once when serve starts; a real tool checks its secrets here."""
