import json


def _count_words(text):
    return len(str(text).split())


def _count_sentences(text):
    text = str(text).strip()
    count = 0
    for index, character in enumerate(text):
        if character not in ".!?":
            continue
        if index == len(text) - 1 or text[index + 1].isspace():
            count += 1
    return count


def _range_matches(value, constraint):
    if not isinstance(constraint, dict):
        return False
    if "exact" in constraint and value != int(constraint["exact"]):
        return False
    if "min" in constraint and value < int(constraint["min"]):
        return False
    if "max" in constraint and value > int(constraint["max"]):
        return False
    return True


def evaluate_contract(response, validation_rules):
    if isinstance(validation_rules, str):
        validation_rules = json.loads(validation_rules)
    if not isinstance(validation_rules, dict):
        return False

    text = str(response)
    normalized = text.strip().lower()

    if "word_count" in validation_rules:
        if not _range_matches(_count_words(text), validation_rules["word_count"]):
            return False

    if "sentence_count" in validation_rules:
        if not _range_matches(_count_sentences(text), validation_rules["sentence_count"]):
            return False

    if "question_count" in validation_rules:
        if not _range_matches(text.count("?"), validation_rules["question_count"]):
            return False

    if "exact_text" in validation_rules:
        expected = str(validation_rules["exact_text"]).strip()
        if text.strip() != expected:
            return False

    if "json_exact" in validation_rules:
        try:
            parsed = json.loads(text)
        except (TypeError, json.JSONDecodeError):
            return False
        if parsed != validation_rules["json_exact"]:
            return False

    for prefix in validation_rules.get("forbidden_prefixes", []):
        if normalized.startswith(str(prefix).lower()):
            return False

    for phrase in validation_rules.get("required_phrases", []):
        if str(phrase).lower() not in normalized:
            return False

    for group in validation_rules.get("required_any_phrases", []):
        if not any(str(phrase).lower() in normalized for phrase in group):
            return False

    for phrase in validation_rules.get("forbidden_phrases", []):
        if str(phrase).lower() in normalized:
            return False

    return True


def grade(sample, item):
    del sample
    if isinstance(item.get("item"), dict):
        item = item["item"]
    return 1.0 if evaluate_contract(item.get("response", ""), item.get("validation_rules")) else 0.0
