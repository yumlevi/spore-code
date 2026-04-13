"""Parse agent questions from QUESTIONS: blocks only."""

import re


def parse_questions(text: str) -> list:
    """Parse structured questions ONLY from an explicit QUESTIONS: block.

    The agent MUST use the exact marker 'QUESTIONS:' on its own line.
    Without this marker, no questions are parsed (prevents false positives
    from numbered lists in regular responses).

    Formats inside the QUESTIONS block:
      1. Single select? [React / Vue / Svelte]
      2. Multi select? {React / Vue / Svelte / Angular}
      3. Open-ended question?

    Only [...] and {...} are recognized as option brackets — NOT parentheses.
    """
    # REQUIRE the QUESTIONS: marker — no fallback to full text
    blocks = re.split(r'(?:^|\n)\s*QUESTIONS?\s*:\s*\n', text, flags=re.IGNORECASE)
    if len(blocks) < 2:
        return []  # No QUESTIONS: marker found → no questions

    q_text = blocks[-1]

    # Split on blank lines, take the first non-empty segment that has numbered items
    segments = re.split(r'\n\s*\n', q_text)
    q_text = ''
    for seg in segments:
        if re.search(r'^\s*\d+\.', seg, re.MULTILINE):
            q_text = seg
            break
    if not q_text:
        return []

    questions = []
    pattern = r'^\s*(\d+)\.\s+(.+?)$'
    for m in re.finditer(pattern, q_text, re.MULTILINE):
        idx = int(m.group(1))
        raw = m.group(2).strip()

        options = None
        multi = False

        # Multi-select: {opt1 / opt2 / opt3} — split on " / " (spaced) to avoid
        # breaking on slashes inside file paths like test_input/drs/
        multi_match = re.search(r'\{([^}]+ / [^}]+)\}', raw)
        if multi_match:
            opts_str = multi_match.group(1)
            options = [o.strip() for o in opts_str.split(' / ') if o.strip()]
            question_text = raw[:multi_match.start()].strip().rstrip('?').strip() + '?'
            multi = True
        else:
            # Single-select: [opt1 / opt2 / opt3] — split on " / " (spaced)
            single_match = re.search(r'\[([^\]]+ / [^\]]+)\]', raw)
            if single_match:
                opts_str = single_match.group(1)
                options = [o.strip() for o in opts_str.split(' / ') if o.strip()]
                question_text = raw[:single_match.start()].strip().rstrip('?').strip() + '?'
            else:
                question_text = raw.rstrip('?').strip() + '?'

        questions.append({
            'text': question_text,
            'options': options if options and len(options) > 1 else None,
            'multi': multi,
            'index': idx,
        })

    return questions


def format_answers(questions: list, answers_data) -> str:
    """Format collected answers into a message to send back to the agent."""
    if isinstance(answers_data, dict) and 'answers' in answers_data:
        answers = answers_data['answers']
        notes = answers_data.get('notes', {})
    else:
        answers = answers_data if isinstance(answers_data, dict) else {}
        notes = {}

    lines = ['Here are my answers to your questions:\n']
    for i, q in enumerate(questions):
        answer = answers.get(i, '(skipped)')
        if isinstance(answer, list):
            answer_str = ', '.join(answer)
        else:
            answer_str = str(answer)

        lines.append(f'{i + 1}. {q["text"]}')
        lines.append(f'   → {answer_str}')

        note = notes.get(i)
        if note:
            lines.append(f'   Note: {note}')
        lines.append('')

    return '\n'.join(lines)
