#!/usr/bin/env python3
"""Validate GitHub pull-request commit authors, committers, and co-author trailers."""
import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

NAME = "Mojtaba Arvin"
EMAIL = "m.arvin@live.com"
LOGIN = "mojtaba-arvin"
MARKER = "<!-- maintainer-directed-ai-work -->"
AUTO_LABEL = "allow-automation-commits"
AUTOMATION = {
    "github-actions[bot]": {
        "email": "41898282+github-actions[bot]@users.noreply.github.com",
    },
    "dependabot[bot]": {
        "email": "49699333+dependabot[bot]@users.noreply.github.com",
    },
}
# These are explicit product/agent identifiers, not a generic substring such as
# "ai" or "gpt" that could reject an unrelated human name or email address.
AGENT_MARKERS = (
    "ai agent", "ai-agent", "agent bot", "assistant bot", "ai assistant",
    "copilot", "claude", "cursor", "windsurf", "codex", "chatgpt",
    "gemini", "aider", "tabnine", "codeium", "devin", "openhands",
    "open hands", "opencode", "roo code", "sourcegraph cody",
    "amazon q", "replit agent", "sweep ai", "junie", "qodo",
    "augment code", "factory ai", "goose ai", "continue.dev",
)
COAUTHOR = re.compile(r"^\s*Co-authored-by:\s*(.*?)\s*<([^<>]+)>\s*$", re.I)


def is_prohibited_identity(name, email, login=None, account_type=None):
    if (account_type or "").lower() == "bot":
        return True
    if login and login.lower().endswith("[bot]"):
        return True
    value = " ".join((name or "", email or "", login or "")).casefold()
    if "[bot]" in value or any(marker in value for marker in AGENT_MARKERS):
        return True
    return False


def automation_identity_is_genuine(login, account_type, name, email):
    expected = AUTOMATION.get(login or "")
    return bool(
        expected
        and (account_type or "").lower() == "bot"
        and (name or "").casefold() == login.casefold()
        and (email or "").casefold() == expected["email"].casefold()
    )


def validate(pr, commits):
    errors = []
    labels = {item.get("name", "") for item in pr.get("labels", [])}
    allow_automation = AUTO_LABEL in labels
    maintainer_work = MARKER in (pr.get("body") or "")
    maintainer_author_seen = False

    for item in commits:
        commit = item.get("commit") or {}
        author = commit.get("author") or {}
        committer = commit.get("committer") or {}
        github_author = item.get("author") or {}
        github_committer = item.get("committer") or {}
        sha = (item.get("sha") or "unknown")[:12]

        author_name = author.get("name") or ""
        author_email = author.get("email") or ""
        author_login = github_author.get("login")
        author_type = github_author.get("type")
        committer_name = committer.get("name") or ""
        committer_email = committer.get("email") or ""
        committer_login = github_committer.get("login")
        committer_type = github_committer.get("type")

        if not author_name or not author_email:
            errors.append(f"{sha}: missing Git author name/email")
        if not committer_name or not committer_email:
            errors.append(f"{sha}: missing Git committer name/email")

        author_is_automation = automation_identity_is_genuine(
            author_login, author_type, author_name, author_email
        )
        committer_is_automation = automation_identity_is_genuine(
            committer_login, committer_type, committer_name, committer_email
        )
        automation_allowed = (
            allow_automation
            and author_is_automation
            and committer_is_automation
            and author_login == committer_login
        )

        if is_prohibited_identity(
            author_name, author_email, author_login, author_type
        ) and not automation_allowed:
            errors.append(
                f"{sha}: prohibited bot/agent author: "
                f"{author_name!r} <{author_email}> login={author_login!r}"
            )

        # GitHub's web-flow committer is a platform operation, not the commit
        # author. It is allowed for otherwise-valid human-authored commits.
        web_flow_committer = committer_login == "web-flow"
        if is_prohibited_identity(
            committer_name, committer_email, committer_login, committer_type
        ) and not (automation_allowed or web_flow_committer):
            errors.append(
                f"{sha}: prohibited bot/agent committer: "
                f"{committer_name!r} <{committer_email}> "
                f"login={committer_login!r}"
            )
        if (committer_type or "").lower() == "bot" and not (
            automation_allowed or web_flow_committer
        ):
            errors.append(f"{sha}: unapproved bot committer {committer_login!r}")

        if author_login == LOGIN:
            maintainer_author_seen = True
            if author_name != NAME or author_email.casefold() != EMAIL.casefold():
                errors.append(
                    f"{sha}: maintainer author must be {NAME} <{EMAIL}>, "
                    f"got {author_name!r} <{author_email}>"
                )
        elif author_email.casefold() == EMAIL.casefold():
            errors.append(
                f"{sha}: maintainer email must resolve to {LOGIN}, "
                f"not {author_login!r}"
            )

        for line in (commit.get("message") or "").splitlines():
            if not line.casefold().lstrip().startswith("co-authored-by:"):
                continue
            match = COAUTHOR.match(line)
            if not match:
                errors.append(f"{sha}: malformed Co-authored-by trailer")
                continue
            coauthor_name, coauthor_email = match.groups()
            if is_prohibited_identity(coauthor_name, coauthor_email):
                errors.append(
                    f"{sha}: prohibited bot/agent Co-authored-by trailer: "
                    f"{coauthor_name!r} <{coauthor_email}>"
                )

    if maintainer_work and not maintainer_author_seen:
        errors.append(
            "maintainer-directed AI work marker requires at least one commit "
            f"authored by {LOGIN}"
        )
    return errors


def get_json(url, token):
    request = urllib.request.Request(
        url,
        headers={
            "Accept": "application/vnd.github+json",
            "Authorization": "Bearer " + token,
            "X-GitHub-Api-Version": "2022-11-28",
            "User-Agent": "gh-fleet-commit-identity-policy",
        },
    )
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.loads(response.read().decode())


def self_test():
    def item(author, committer, author_account, committer_account, message="feat: test"):
        return {
            "sha": "a" * 40,
            "commit": {"author": author, "committer": committer, "message": message},
            "author": author_account,
            "committer": committer_account,
        }

    maintainer = {"name": NAME, "email": EMAIL}
    human = {"name": "External Human", "email": "human@example.org"}
    web_flow = {"name": "GitHub", "email": "noreply@github.com"}
    maintainer_account = {"login": LOGIN, "type": "User"}
    human_account = {"login": "external-human", "type": "User"}
    web_flow_account = {"login": "web-flow", "type": "User"}

    assert not validate(
        {"body": "", "labels": []},
        [item(maintainer, maintainer, maintainer_account, maintainer_account)],
    )
    assert not validate(
        {"body": "", "labels": []},
        [item(human, web_flow, human_account, web_flow_account)],
    )
    assert validate(
        {"body": "", "labels": []},
        [item(
            {"name": "Copilot", "email": "copilot@example.org"},
            web_flow, {"login": "copilot[bot]", "type": "Bot"}, web_flow_account,
        )],
    )
    assert validate(
        {"body": "", "labels": []},
        [item(
            human, {"name": "Claude", "email": "claude@example.org"},
            human_account, {"login": "claude[bot]", "type": "Bot"},
        )],
    )
    assert validate(
        {"body": "", "labels": []},
        [item(
            human, web_flow, human_account, web_flow_account,
            "feat: test\n\nCo-authored-by: Copilot <copilot[bot]@users.noreply.github.com>",
        )],
    )
    assert not validate(
        {"body": MARKER, "labels": []},
        [item(maintainer, maintainer, maintainer_account, maintainer_account)],
    )
    assert validate(
        {"body": MARKER, "labels": []},
        [item(human, web_flow, human_account, web_flow_account)],
    )
    assert validate(
        {"body": "", "labels": []},
        [item(
            {"name": NAME, "email": "wrong@example.org"},
            maintainer, maintainer_account, maintainer_account,
        )],
    )
    actions = {
        "name": "github-actions[bot]",
        "email": AUTOMATION["github-actions[bot]"]["email"],
    }
    actions_account = {"login": "github-actions[bot]", "type": "Bot"}
    assert not validate(
        {"body": "", "labels": [{"name": AUTO_LABEL}]},
        [item(actions, actions, actions_account, actions_account)],
    )
    assert validate(
        {"body": "", "labels": []},
        [item(actions, actions, actions_account, actions_account)],
    )
    assert validate(
        {"body": "", "labels": [{"name": AUTO_LABEL}]},
        [item(actions, maintainer, actions_account, maintainer_account)],
    )
    assert validate(
        {"body": "", "labels": []},
        [item(maintainer, {}, maintainer_account, {})],
    )
    print("PASS: commit author, committer, trailer, and automation policy self-tests")


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--repository", default=os.getenv("GITHUB_REPOSITORY"))
    parser.add_argument("--pr-number", type=int, default=int(os.getenv("PR_NUMBER", "0")))
    parser.add_argument("--token", default=os.getenv("GITHUB_TOKEN"))
    parser.add_argument("--self-test", action="store_true")
    args = parser.parse_args()
    if args.self_test:
        self_test()
    if not (args.repository and args.pr_number and args.token):
        if args.self_test and not args.pr_number:
            return 0
        parser.error("--repository, --pr-number and --token are required")

    base = "https://api.github.com/repos/" + args.repository
    pr = get_json(base + "/pulls/" + str(args.pr_number), args.token)
    commits = []
    page = 1
    while True:
        batch = get_json(
            base + f"/pulls/{args.pr_number}/commits?per_page=100&page={page}",
            args.token,
        )
        if not isinstance(batch, list):
            raise RuntimeError("GitHub API returned a non-list commit response")
        commits.extend(batch)
        if len(batch) < 100:
            break
        page += 1
    if not commits:
        print("ERROR: empty PR commit list; refusing to pass")
        return 2

    errors = validate(pr, commits)
    print(f"Checked {len(commits)} commits in {args.repository}#{args.pr_number}")
    if errors:
        print("\n".join("ERROR: " + error for error in errors))
        return 1
    print("PASS: no prohibited bot/agent authors, committers, or co-author trailers")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except urllib.error.HTTPError as error:
        print(f"ERROR: GitHub API HTTP {error.code}: {error.reason}", file=sys.stderr)
        sys.exit(2)
    except (urllib.error.URLError, TimeoutError, ValueError, RuntimeError) as error:
        print(f"ERROR: identity validation incomplete: {error}", file=sys.stderr)
        sys.exit(2)
