import { TextField } from '@mui/material';
import { Meta, Story } from '@storybook/react';
import React from 'react';
import { PluginInfo, PluginSettingsDetailProps } from '../../../plugin/pluginsSlice';
import { PluginSettingsDetailPure, PluginSettingsDetailPureProps } from './pluginSettingsDetail';

const testAutoSaveComponent: React.FC<PluginSettingsDetailProps> = () => {
  const [data, setData] = React.useState<{ [key: string]: any }>({});
  const onChange = (value: string) => {
    console.log(value);
    setData({ works: value });
  };

  return (
    <TextField
      value={data?.works || ''}
      onChange={e => onChange(e.target.value)}
      label="Normal Input"
      variant="outlined"
      fullWidth
    />
  );
};

const testNormalComponent: React.FC<PluginSettingsDetailProps> = props => {
  const { data, onDataChange } = props;

  function onChange(value: string) {
    if (onDataChange) {
      onDataChange({ works: value });
    }
  }

  return (
    <TextField
      value={data?.works || ''}
      onChange={e => onChange(e.target.value)}
      label="Normal Input"
      variant="outlined"
      fullWidth
    />
  );
};

// Mock PluginInfo data
const mockPluginInfoAutoSave: PluginInfo = {
  name: 'Example Plugin AutoSave',
  description: 'This is an example plugin with auto-save enabled.',
  version: '0.0.1',
  homepage: 'https://example.com/plugin-auto-save',
  settingsComponent: testAutoSaveComponent,
  settingsAutoSave: true,
};

const mockPluginInfoNormal: PluginInfo = {
  name: 'Example Plugin Normal',
  description: 'This is an example plugin with normal save.',
  version: '0.0.1',
  homepage: 'https://example.com/plugin-normal',
  settingsComponent: testNormalComponent,
  settingsAutoSave: false,
};

const mockConfig = {
  name: 'mockPlugin',
};

const Template: Story<PluginSettingsDetailPureProps> = (args: PluginSettingsDetailPureProps) => (
  <PluginSettingsDetailPure {...args} />
);

export const WithAutoSave = Template.bind({});
WithAutoSave.args = {
  plugin: mockPluginInfoAutoSave,
  onDelete: () => console.log('Delete action'),
};

export const WithoutAutoSave = Template.bind({});
WithoutAutoSave.args = {
  config: mockConfig,
  plugin: mockPluginInfoNormal,
  onSave: (data: { [key: string]: any }) => console.log('Save data:', data),
  onDelete: () => console.log('Delete action'),
};

export default {
  title: 'Settings/PluginSettingsDetail',
  component: PluginSettingsDetailPure,
} as Meta;
