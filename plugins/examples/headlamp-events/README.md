# Example Plugin: Reacting to Headlamp events

Headlamp has the concept of Headlamp events, which are fired when something relevant happens in Headlamp.
For example, when a user chooses to apply the changes their changing for a resource, or when a
list/details view is loaded.

To run the plugin:

```bash
cd plugins/examples/change-logo
npm install
npm start
# Go to a list or details view to see one example of a Headlamp event.
```

- The plugin shows a notification whenever there is a Headlamp event, except for OBJECT_EVENTS type of
  events (so checking the event type is demonstrated).
- For events that have a resource, the resource name is shown.
