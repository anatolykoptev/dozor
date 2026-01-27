"""
Log analyzer with service-specific patterns.

Includes bot scanner detection to reduce false positives.
"""

import re
from dataclasses import dataclass
from typing import Optional

from ..models import LogEntry, AlertLevel


@dataclass
class ErrorPattern:
    """Definition of an error pattern to detect."""
    pattern: str
    level: AlertLevel
    category: str
    description: str
    suggested_action: str
    services: Optional[list[str]] = None  # None = applies to all


# Known bot scanner paths - these generate 404s that are NOT errors
# These are malicious scanners looking for vulnerable endpoints
BOT_SCANNER_PATHS = [
    # SDK/language probes (China-based scanners)
    r'/SDK/webLanguage',
    r'/sdk/',
    # WordPress probes
    r'/wp-admin',
    r'/wp-login',
    r'/wp-content',
    r'/wp-includes',
    r'/xmlrpc\.php',
    # PHP probes
    r'\.php$',
    r'/phpmyadmin',
    r'/phpMyAdmin',
    r'/pma',
    # Config file probes
    r'/\.env',
    r'/\.git',
    r'/config\.json',
    r'/\.aws',
    r'/\.ssh',
    # Admin panel probes
    r'/admin',
    r'/administrator',
    r'/manager',
    r'/console',
    # Backup file probes
    r'\.bak$',
    r'\.backup$',
    r'\.sql$',
    r'\.tar\.gz$',
    r'\.zip$',
    # API vulnerability probes
    r'/api/v\d+/swagger',
    r'/swagger',
    r'/actuator',
    r'/metrics',
    r'/debug',
    # Well-known vulnerability paths
    r'/cgi-bin',
    r'/shell',
    r'/cmd',
    r'/eval',
    r'/exec',
]

# Compile patterns once for performance
_BOT_SCANNER_PATTERNS = [re.compile(p, re.IGNORECASE) for p in BOT_SCANNER_PATHS]


def is_bot_scanner_request(log_message: str) -> bool:
    """Check if a log entry is from a bot scanner (should be ignored)."""
    for pattern in _BOT_SCANNER_PATTERNS:
        if pattern.search(log_message):
            return True
    return False


class LogAnalyzer:
    """Analyzes logs for known error patterns."""

    # Service-specific error patterns
    PATTERNS = [
        # PostgreSQL patterns
        ErrorPattern(
            pattern=r'FATAL:\s+(?:the database system is|password authentication failed)',
            level=AlertLevel.CRITICAL,
            category='database',
            description='PostgreSQL authentication or startup failure',
            suggested_action='Check PostgreSQL credentials and container health. Run: docker compose logs postgres',
            services=['postgres'],
        ),
        ErrorPattern(
            pattern=r'ERROR:\s+(?:relation|column|table)\s+"[^"]+"\s+(?:does not exist|already exists)',
            level=AlertLevel.ERROR,
            category='database',
            description='Database schema error - missing or duplicate object',
            suggested_action='Run database migrations or check schema definitions',
            services=['postgres', 'hasura'],
        ),
        ErrorPattern(
            pattern=r'FATAL:\s+too many connections',
            level=AlertLevel.CRITICAL,
            category='database',
            description='PostgreSQL connection pool exhausted',
            suggested_action='Increase max_connections or investigate connection leaks',
            services=['postgres'],
        ),
        ErrorPattern(
            pattern=r'collation version mismatch',
            level=AlertLevel.WARNING,
            category='database',
            description='PostgreSQL collation version mismatch',
            suggested_action='Run REINDEX DATABASE and ALTER DATABASE REFRESH COLLATION VERSION',
            services=['postgres'],
        ),

        # Hasura patterns
        ErrorPattern(
            pattern=r'inconsistent object|metadata is inconsistent',
            level=AlertLevel.ERROR,
            category='graphql',
            description='Hasura metadata inconsistency',
            suggested_action='Run Hasura console and reload metadata. Check database schema changes.',
            services=['hasura'],
        ),
        ErrorPattern(
            pattern=r'JWT.*(?:expired|invalid|malformed)',
            level=AlertLevel.ERROR,
            category='auth',
            description='JWT authentication failure',
            suggested_action='Check JWT secret configuration matches between services',
            services=['hasura', 'supabase-auth'],
        ),

        # n8n patterns
        ErrorPattern(
            pattern=r'Workflow\s+(?:execution\s+)?failed',
            level=AlertLevel.WARNING,
            category='workflow',
            description='n8n workflow execution failed',
            suggested_action='Check workflow logs for specific error. Review node configurations.',
            services=['n8n'],
        ),
        ErrorPattern(
            pattern=r'ECONNREFUSED|Connection refused',
            level=AlertLevel.ERROR,
            category='network',
            description='Connection refused to dependent service',
            suggested_action='Check if dependent service is running. Verify network configuration.',
            services=['n8n'],
        ),
        ErrorPattern(
            pattern=r'credential.*(?:not found|invalid|missing)',
            level=AlertLevel.ERROR,
            category='credentials',
            description='n8n credential not found or invalid',
            suggested_action='Re-configure credentials in n8n. Check API keys/tokens.',
            services=['n8n'],
        ),

        # Supabase Auth patterns
        ErrorPattern(
            pattern=r'GoTrue.*(?:error|failed)',
            level=AlertLevel.ERROR,
            category='auth',
            description='Supabase Auth (GoTrue) error',
            suggested_action='Check GoTrue configuration and database connection',
            services=['supabase-auth'],
        ),
        ErrorPattern(
            pattern=r'OAuth.*(?:failed|error)',
            level=AlertLevel.ERROR,
            category='auth',
            description='OAuth authentication failure',
            suggested_action='Verify OAuth provider credentials (GitHub client ID/secret)',
            services=['supabase-auth'],
        ),

        # Embedding service patterns
        ErrorPattern(
            pattern=r'CUDA.*error|GPU.*(?:not available|out of memory)',
            level=AlertLevel.ERROR,
            category='gpu',
            description='GPU/CUDA error in embedding service',
            suggested_action='Check GPU availability. May need to restart service or reduce batch size.',
            services=['embedding-service'],
        ),
        ErrorPattern(
            pattern=r'model.*(?:not found|failed to load)',
            level=AlertLevel.CRITICAL,
            category='model',
            description='ML model loading failure',
            suggested_action='Check model path and download. Verify disk space.',
            services=['embedding-service'],
        ),

        # Generic patterns (apply to all services)
        ErrorPattern(
            pattern=r'OOM|Out of memory|Cannot allocate memory',
            level=AlertLevel.CRITICAL,
            category='resources',
            description='Out of memory condition',
            suggested_action='Increase container memory limits or investigate memory leak',
        ),
        ErrorPattern(
            pattern=r'disk.*(?:full|space)|No space left on device',
            level=AlertLevel.CRITICAL,
            category='resources',
            description='Disk space exhausted',
            suggested_action='Clear old logs/data. Run docker system prune. Increase disk.',
        ),
        ErrorPattern(
            pattern=r'SIGTERM|SIGKILL|killed',
            level=AlertLevel.WARNING,
            category='process',
            description='Process was terminated by signal',
            suggested_action='Check if intentional. May indicate OOM killer or manual intervention.',
        ),
        ErrorPattern(
            pattern=r'timeout|timed out|deadline exceeded',
            level=AlertLevel.WARNING,
            category='performance',
            description='Operation timeout',
            suggested_action='Check service performance. May need to increase timeout or optimize queries.',
        ),
        ErrorPattern(
            pattern=r'rate limit|too many requests|429',
            level=AlertLevel.WARNING,
            category='rate_limit',
            description='Rate limit exceeded',
            suggested_action='Reduce request frequency or increase rate limits',
        ),
    ]

    def __init__(self):
        # Compile patterns for performance
        self._compiled = [
            (re.compile(p.pattern, re.IGNORECASE), p)
            for p in self.PATTERNS
        ]

    def analyze_entry(self, entry: LogEntry) -> list[ErrorPattern]:
        """Analyze a single log entry for known patterns."""
        # Skip bot scanner requests (404s on known malicious paths)
        if is_bot_scanner_request(entry.raw):
            return []

        matches = []

        for compiled_pattern, pattern in self._compiled:
            # Check if pattern applies to this service
            if pattern.services and entry.service not in pattern.services:
                continue

            if compiled_pattern.search(entry.raw):
                matches.append(pattern)

        return matches

    def analyze_logs(self, entries: list[LogEntry]) -> dict:
        """
        Analyze multiple log entries.

        Returns:
            {
                'total_entries': int,
                'errors_found': int,
                'patterns_matched': [
                    {
                        'pattern': ErrorPattern,
                        'count': int,
                        'sample_entries': list[LogEntry],
                    }
                ],
                'by_category': {
                    'database': int,
                    'auth': int,
                    ...
                }
            }
        """
        pattern_counts = {}
        pattern_samples = {}
        category_counts = {}

        for entry in entries:
            matched = self.analyze_entry(entry)

            for pattern in matched:
                key = pattern.pattern

                pattern_counts[key] = pattern_counts.get(key, 0) + 1

                if key not in pattern_samples:
                    pattern_samples[key] = {'pattern': pattern, 'samples': []}
                if len(pattern_samples[key]['samples']) < 3:
                    pattern_samples[key]['samples'].append(entry)

                category_counts[pattern.category] = category_counts.get(pattern.category, 0) + 1

        return {
            'total_entries': len(entries),
            'errors_found': sum(pattern_counts.values()),
            'patterns_matched': [
                {
                    'pattern': data['pattern'],
                    'count': pattern_counts[key],
                    'sample_entries': data['samples'],
                }
                for key, data in pattern_samples.items()
            ],
            'by_category': category_counts,
        }

    def get_most_critical(self, entries: list[LogEntry]) -> Optional[ErrorPattern]:
        """Get the most critical pattern from log entries."""
        analysis = self.analyze_logs(entries)

        if not analysis['patterns_matched']:
            return None

        # Sort by level (critical > error > warning)
        level_order = {
            AlertLevel.CRITICAL: 0,
            AlertLevel.ERROR: 1,
            AlertLevel.WARNING: 2,
            AlertLevel.INFO: 3,
        }

        sorted_patterns = sorted(
            analysis['patterns_matched'],
            key=lambda x: (level_order.get(x['pattern'].level, 99), -x['count'])
        )

        return sorted_patterns[0]['pattern'] if sorted_patterns else None
